package runner

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"github.com/dizziedbliss/meow-pspsps/internal/mock"
)

// BaseImage is the pinned upstream n8n image. JudgeImage is the baked image built from it.
const (
	N8nVersion = "2.47.4"
	BaseImage  = "docker.n8n.io/n8nio/n8n:" + N8nVersion
	JudgeImage = "meow-judge-n8n:" + N8nVersion
	MockImage  = "meow-judge-mock:" + N8nVersion
	netName    = "meow-judge-net"
	netSubnet  = "172.30.99.0/24"
	mockIP     = "172.30.99.2"
	caPath     = "/home/node/judge-ca.pem"
	apiKeyLbl  = "meow.judge.apikey"
	userFolder = "/home/node/judge" // not a declared volume, so `docker commit` keeps it
)

// containerEnv is the n8n config every judge container (and the bake container) runs with.
func containerEnv(execTimeoutSec int) []string {
	return []string{
		"N8N_USER_FOLDER=" + userFolder,
		"N8N_DIAGNOSTICS_ENABLED=false",
		"N8N_VERSION_NOTIFICATIONS_ENABLED=false",
		"N8N_PERSONALIZATION_ENABLED=false",
		"N8N_COMMUNITY_PACKAGES_ENABLED=false",
		"N8N_BLOCK_ENV_ACCESS_IN_NODE=true",
		`NODES_EXCLUDE=["n8n-nodes-base.executeCommand","n8n-nodes-base.localFileTrigger","n8n-nodes-base.ssh","n8n-nodes-base.ftp","n8n-nodes-base.git"]`,
		fmt.Sprintf("EXECUTIONS_TIMEOUT=%d", execTimeoutSec),
		"GENERIC_TIMEZONE=UTC",
		"TZ=UTC",
		"N8N_SECURE_COOKIE=false",
		"NODE_OPTIONS=--max-old-space-size=1024",
		"N8N_RUNNERS_MAX_OLD_SPACE_SIZE=512",
	}
}

func docker(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "docker", args...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("docker %s: %v: %s", args[0], err, strings.TrimSpace(errb.String()))
	}
	return strings.TrimSpace(out.String()), nil
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// container is one running judge n8n.
type container struct {
	Name string
	Base string // http://127.0.0.1:<port>
}

type runOpts struct {
	image          string
	execTimeoutSec int
	memory         string
	cpus           string
	keep           bool // no --rm, so the container survives `docker stop` (bake commits it)
	withMock       bool // join the judge network and send Google API hosts to the mock
}

// startContainer runs an n8n container bound to a random localhost port and waits until ready.
func startContainer(ctx context.Context, o runOpts) (*container, error) {
	name := "meow-judge-" + randHex(4)
	args := []string{"run", "-d", "--name", name,
		"-p", "127.0.0.1::5678",
		"--memory", o.memory, "--cpus", o.cpus, "--pids-limit", "512",
		"--cap-drop", "ALL", "--security-opt", "no-new-privileges",
		"--label", "meow.judge=1",
	}
	if o.withMock {
		args = append(args, "--network", netName)
		for _, h := range mock.GoogleHosts {
			args = append(args, "--add-host", h+":"+mockIP)
		}
	}
	if !o.keep {
		args = append(args, "--rm")
	}
	for _, e := range containerEnv(o.execTimeoutSec) {
		args = append(args, "-e", e)
	}
	args = append(args, o.image)
	if _, err := docker(ctx, args...); err != nil {
		return nil, err
	}
	c := &container{Name: name}
	port, err := docker(ctx, "port", name, "5678/tcp")
	if err != nil {
		c.destroy()
		return nil, err
	}
	// "127.0.0.1:49153" (may list an IPv6 line too)
	first := strings.SplitN(port, "\n", 2)[0]
	c.Base = "http://" + first
	if err := waitReady(ctx, c.Base, 3*time.Minute); err != nil {
		logs, _ := docker(context.Background(), "logs", "--tail", "20", name)
		c.destroy()
		return nil, fmt.Errorf("%v; last logs:\n%s", err, logs)
	}
	return c, nil
}

func waitReady(ctx context.Context, base string, max time.Duration) error {
	deadline := time.Now().Add(max)
	hc := &http.Client{Timeout: 3 * time.Second}
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		resp, err := hc.Get(base + "/healthz/readiness")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return nil
			}
		}
		time.Sleep(400 * time.Millisecond)
	}
	return fmt.Errorf("n8n at %s not ready after %s", base, max)
}

func (c *container) destroy() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, _ = docker(ctx, "rm", "-f", c.Name)
}

// imageAPIKey reads the API key baked into the judge image's label.
func imageAPIKey(ctx context.Context, image string) (string, error) {
	k, err := docker(ctx, "image", "inspect", "--format", "{{index .Config.Labels \""+apiKeyLbl+"\"}}", image)
	if err != nil {
		return "", fmt.Errorf("judge image %s not found; run `judge bake` on this machine first (%v)", image, err)
	}
	if k == "" || k == "<no value>" {
		return "", fmt.Errorf("image %s has no API key label; re-run `judge bake`", image)
	}
	return k, nil
}

// ensureMock makes sure the judge network and this worker's mock container are up, and returns the
// mock's admin URL on localhost.
func ensureMock(ctx context.Context) (adminURL, token string, err error) {
	if _, err := docker(ctx, "network", "inspect", netName); err != nil {
		if _, err := docker(ctx, "network", "create", "--subnet", netSubnet, "--label", "meow.judge.net=1", netName); err != nil {
			return "", "", fmt.Errorf("create network %s: %w", netName, err)
		}
	}
	if _, err := docker(ctx, "image", "inspect", MockImage); err != nil {
		return "", "", fmt.Errorf("mock image %s missing; re-run `judge bake`", MockImage)
	}
	token = randHex(24) // per start; only this worker's runner knows it
	name := "meow-judge-mock"
	_, _ = docker(ctx, "rm", "-f", name)
	if _, err := docker(ctx, "run", "-d", "--rm", "--name", name, "--network", netName, "--ip", mockIP,
		"-p", "127.0.0.1::8080", "--memory", "256m", "--pids-limit", "256",
		"--cap-drop", "ALL", "--cap-add", "NET_BIND_SERVICE", "--security-opt", "no-new-privileges",
		"-e", "MOCK_ADMIN_TOKEN="+token, "--label", "meow.judge=1", MockImage); err != nil {
		return "", "", err
	}
	port, err := docker(ctx, "port", name, "8080/tcp")
	if err != nil {
		return "", "", err
	}
	base := "http://" + strings.SplitN(port, "\n", 2)[0]
	hc := &http.Client{Timeout: 2 * time.Second}
	for i := 0; i < 50; i++ {
		if resp, err := hc.Get(base + "/healthz"); err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return base, token, nil
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	logs, _ := docker(context.Background(), "logs", "--tail", "20", name)
	return "", "", fmt.Errorf("mock didn't start; logs:\n%s", logs)
}
