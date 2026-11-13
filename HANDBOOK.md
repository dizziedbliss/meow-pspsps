# VeryJudgyCat event handbook

Use this for the Automathon event. The organiser laptop is the **central machine**; college Ubuntu PCs are **workers** that run the tests. Keep the central machine, internet and tunnel running during the event; plug it in and disable automatic sleep. Keep hidden tests (your `problems/` packs) out of git: they are gitignored.

## Before going to college

1. Bring the central laptop, charger, internet access, and a USB drive containing the whole `dist/worker-bundle` folder. The worker PCs need 64-bit Ubuntu/Debian Linux, systemd, sudo, internet access, and Docker. The bundle contains the worker binary and the two baked images; cloning the repository alone does **not** contain those images or the organiser's private configuration.
2. On the central laptop, keep `~/.config/meow-judge/` and `event.db` intact. They contain the grader setup, secrets and event records. Do not upload them or `teams.csv` to GitHub. `teams.csv` contains the team codes and passwords; distribute each team only its own login.
3. The grader uses real model API calls. Telegram, Gmail and Google Sheets are **simulated** for these two problems, so no emails/messages go to real people and no actual spreadsheet is modified. Teams need their own working AI key when their workflow uses an AI service; ensure they have enough quota.
4. Keep an organiser-only copy of every test case (hidden tests included) on your own machine, outside git.
5. Hand teams only each round's `statement.md` or a PDF made from it.

## At college: central machine

Open a terminal on the central laptop (WSL if it runs Windows) and go to the project:

```bash
cd path/to/meow-pspsps
```

Check whether the central site is already running:

```bash
curl --noproxy '*' -fsS http://127.0.0.1:8080/healthz
```

If it is already up, leave it and its tunnel running. Otherwise, start it with:

```bash
./deploy/start-central.sh --pool 2
```

Record the `https://...trycloudflare.com` address it prints. The address of a quick tunnel changes when it restarts: every worker must be updated with the new address. If an already-running tunnel is being kept, restart only the site with `./deploy/start-central.sh --pool 2 --no-tunnel`. Never run two central pools or a separate `judge check` on the same Docker host: they remove one another's judge containers.

The admin password is stored locally in `~/.config/meow-judge/admin.pw`; the separate worker token is in `~/.config/meow-judge/worker.token`. Read them privately on the central laptop when needed. Do not post either in a group chat or include them in a public repository. The site and score database are on the central laptop, not in the workers' copied repositories. On a different central machine you must separately provision its private config, baked Docker images and event database; a fork alone cannot restore them.

## At college: worker PCs

First try the following on **one** 64-bit Ubuntu PC, then repeat on the others. The worker script can install Docker itself on Ubuntu/Debian if needed; if Docker is already installed, use `--skip-docker`.

1. Copy the whole `worker-bundle` folder from the USB drive to the PC and open a terminal in that folder.
2. Run:

```bash
sudo ./veryjudgycat.sh install --central https://YOUR-SITE --pool 2 --skip-docker
```

Replace `https://YOUR-SITE` with the current central HTTPS address. The installer prompts privately for the worker token; enter the value from the central laptop. If Docker is not installed and you want the script to install it, omit `--skip-docker`. A 6 GB PC should start with pool 2; use `--pool 1` if it struggles. The installer verifies bundle checksums and starts a systemd service.

3. Check the worker:

```bash
sudo ./veryjudgycat.sh status
```

Look for a running service, two images and a successful central connection. In the organiser page, confirm the worker appears under test machines. If the tunnel address changes, rerun the install command with the new URL; it updates and restarts the worker service.

If you prefer a private fork for the source, fork and clone the repository only with permission and keep it private. The simplest worker setup is still to copy the prepared bundle: it includes the images and does not need Go, GitHub access, the grader keys, or `event.db`.

## Before participants begin

1. Open `https://YOUR-SITE/admin/login`; use the admin password from the central laptop.
2. Set marks per passed test and the maximum for each round. Set which rounds the leaderboard shows. Open round 1 when ready.
3. Confirm that at least one test machine is connected. Open the site from a participant's device using the HTTPS address, log in using a test account, and run a **sample** with a working AI key. Confirm that the run completes and the result page updates. Check the organiser incident list. Do not distribute test account credentials to participants.
4. The private organiser practice account `PPOOL` has unlimited runs, can preview the next pending round, and is excluded from the leaderboard and CSV. Its password is shared separately; keep this account private. The participant guide is linked on the login page and inside the site (`/static/participant-guide.pdf`). Give each team its code and password from the private `teams.csv`. Teams open the HTTPS site, upload their exported n8n JSON, provide their own AI key if needed, and start with the free samples. Each team has three scored attempts per round; the best eligible result counts. Explain that provider rate limits may cause waits or a refunded attempt.
5. When round 1 ends, use the admin page to open round 2. "Open round" closes the previous one. To keep both rounds open (teams switch between tabs and may come back to submit later), use "Open too" instead. Attempts stay 3 per round, and a team can have only one run going at a time. Enter offline scores and export the scoreboard CSV when needed.

## Troubleshooting

- **Page unreachable:** check central power, WSL, internet, and the tunnel. Check central health with the command above. If the tunnel restarts, distribute its new HTTPS address and reinstall/reconfigure each worker against it.
- **Run stays queued:** check the test-machine list and `sudo ./veryjudgycat.sh status` on each worker. At least one worker must be connected and have capacity. Inspect `logs/central.log` on the central machine and `sudo journalctl -u veryjudgycat-worker -n 50 --no-pager` on a worker.
- **Model unavailable / key rejected:** the team must choose an available model and provide its own valid key. The organiser cannot fix a retired model just by restarting the judge.
- **Judge-side failure:** inspect the incident on the admin page. Such failures refund the scored attempt. A central restart fails unfinished runs and refunds them; completed scores remain in `event.db`.
- **Slow or rate-limited grading:** Gemini free keys share project quotas; additional keys only add capacity when they belong to separate projects. Grader keys are read when the central site starts. Change them and restart between rounds, not while runs are active.
- **Backups:** copy `event.db` safely with SQLite's backup command while the site is running, or stop the site before copying it together with any `-wal` and `-shm` files. Never reset the event database during the event.

## After the event

Export the final CSV and back up `event.db`. On each worker run `sudo ./veryjudgycat.sh uninstall --keep-docker` from the bundle to remove the judge service and images while retaining Docker. Rotate the keys used for testing and handle team credentials securely.
