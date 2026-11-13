package runner

import (
	"bytes"
	"context"
	"debug/elf"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"os"
	"path/filepath"
	"time"

	"github.com/dizziedbliss/meow-pspsps/internal/mock"
)

// BakeResult is what a bake produced.
type BakeResult struct {
	Image       string
	NodesJSON   []byte // n8n /types/nodes.json, for regenerating the catalog
	CredsJSON   []byte // n8n /types/credentials.json
	BootSeconds float64
}

// Bake boots the pinned upstream n8n, creates the owner and an API key, snapshots the node and
// credential catalogue, and commits the result as JudgeImage with the API key in a label. Every
// judge container then starts from this state, with no setup per run.
func Bake(ctx context.Context, logf func(string, ...any)) (*BakeResult, error) {
	logf("pulling %s (if needed)", BaseImage)
	if _, err := docker(ctx, "image", "inspect", BaseImage); err != nil {
		if _, err := docker(ctx, "pull", BaseImage); err != nil {
			return nil, err
		}
	}
	t0 := time.Now()
	c, err := startContainer(ctx, runOpts{image: BaseImage, execTimeoutSec: 60, memory: "2g", cpus: "2", keep: true})
	if err != nil {
		return nil, err
	}
	defer c.destroy()
	boot := time.Since(t0).Seconds()
	logf("n8n ready in %.1fs at %s", boot, c.Base)

	jar, _ := cookiejar.New(nil)
	hc := &http.Client{Jar: jar, Timeout: 60 * time.Second}
	post := func(path string, body any) ([]byte, error) {
		buf, _ := json.Marshal(body)
		resp, err := hc.Post(c.Base+path, "application/json", bytes.NewReader(buf))
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		data, _ := io.ReadAll(resp.Body)
		if resp.StatusCode/100 != 2 {
			return nil, fmt.Errorf("POST %s: %d %s", path, resp.StatusCode, data)
		}
		return data, nil
	}
	get := func(path string) ([]byte, error) {
		resp, err := hc.Get(c.Base + path)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		data, _ := io.ReadAll(resp.Body)
		if resp.StatusCode/100 != 2 {
			return nil, fmt.Errorf("GET %s: %d", path, resp.StatusCode)
		}
		return data, nil
	}

	// The owner password only guards a container bound to localhost and thrown away after one run.
	pw := "Judge-" + randHex(8) + "!"
	if _, err := post("/rest/owner/setup", map[string]string{"email": "judge@judge.local", "firstName": "Judge", "lastName": "Bot", "password": pw}); err != nil {
		return nil, err
	}
	if _, err := post("/rest/login", map[string]string{"emailOrLdapLoginId": "judge@judge.local", "password": pw}); err != nil {
		return nil, err
	}
	scopesRaw, err := get("/rest/api-keys/scopes")
	if err != nil {
		return nil, err
	}
	var scopes struct {
		Data []string `json:"data"`
	}
	if err := json.Unmarshal(scopesRaw, &scopes); err != nil {
		return nil, err
	}
	keyRaw, err := post("/rest/api-keys", map[string]any{"label": "judge", "scopes": scopes.Data, "expiresAt": nil})
	if err != nil {
		return nil, err
	}
	var key struct {
		Data struct {
			RawAPIKey string `json:"rawApiKey"`
		} `json:"data"`
	}
	if err := json.Unmarshal(keyRaw, &key); err != nil || key.Data.RawAPIKey == "" {
		return nil, fmt.Errorf("could not read the new API key: %v", err)
	}
	nodes, err := get("/types/nodes.json")
	if err != nil {
		return nil, err
	}
	creds, err := get("/types/credentials.json")
	if err != nil {
		return nil, err
	}

	certs, err := mock.GenerateCerts()
	if err != nil {
		return nil, err
	}
	if err := buildMockImage(ctx, certs, logf); err != nil {
		return nil, err
	}

	logf("committing %s", JudgeImage)
	if _, err := docker(ctx, "stop", "-t", "30", c.Name); err != nil {
		return nil, err
	}
	caFile, err := os.CreateTemp("", "judge-ca-*.pem")
	if err != nil {
		return nil, err
	}
	defer os.Remove(caFile.Name())
	_, _ = caFile.Write(certs.CACert)
	caFile.Close()
	_ = os.Chmod(caFile.Name(), 0o644)
	if _, err := docker(ctx, "cp", caFile.Name(), c.Name+":"+caPath); err != nil {
		return nil, err
	}
	if _, err := docker(ctx, "commit",
		"--change", "ENV NODE_EXTRA_CA_CERTS="+caPath,
		"--change", "LABEL "+apiKeyLbl+"="+key.Data.RawAPIKey,
		"--change", "LABEL meow.judge.n8n="+N8nVersion,
		c.Name, JudgeImage); err != nil {
		return nil, err
	}
	return &BakeResult{Image: JudgeImage, NodesJSON: nodes, CredsJSON: creds, BootSeconds: boot}, nil
}

// buildMockImage packages this very binary (which must be static) with the mock's certificate into
// a scratch image that runs `judge mock`.
func buildMockImage(ctx context.Context, certs *mock.Certs, logf func(string, ...any)) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	if f, err := elf.Open(self); err == nil {
		for _, p := range f.Progs {
			if p.Type == elf.PT_INTERP {
				f.Close()
				return fmt.Errorf("the judge binary is dynamically linked; rebuild it with CGO_ENABLED=0 go build -o bin/judge ./cmd/judge")
			}
		}
		f.Close()
	}
	dir, err := os.MkdirTemp("", "judge-mock-build-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	bin, err := os.ReadFile(self)
	if err != nil {
		return err
	}
	files := map[string][]byte{
		"judge": bin, "leaf.pem": certs.LeafCert, "key.pem": certs.LeafKey,
		"Dockerfile": []byte("FROM scratch\nCOPY judge /judge\nCOPY leaf.pem key.pem /certs/\nEXPOSE 80 443 8080\n" +
			"ENTRYPOINT [\"/judge\", \"mock\", \"--cert\", \"/certs/leaf.pem\", \"--key\", \"/certs/key.pem\"]\n"),
	}
	for name, data := range files {
		mode := os.FileMode(0o644)
		if name == "judge" {
			mode = 0o755
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, mode); err != nil {
			return err
		}
	}
	logf("building %s", MockImage)
	_, err = docker(ctx, "build", "-q", "-t", MockImage, dir)
	return err
}
