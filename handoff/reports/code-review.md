# Event build: focused follow-up review

The earlier automated review stopped on an API rate limit and produced no completed review. This is a focused manual follow-up, not a comprehensive security audit.

## Fixed

- A webhook could answer immediately and leave its n8n execution running beyond the timeout while the runner graded incomplete side effects. The runner now marks that step timed out when it cannot observe completion; regression coverage was added.
- A rubric grader response containing `{}` could be interpreted as a negative decision and wrongly penalise a team. A missing `pass` decision is now a judge error and can trigger provider failover/refund.
- Team history and leaderboard queries loaded every full workflow and verdict body only to discard them. The list queries now avoid those large fields; detailed run views retain the full data.
- Reinstalling a worker after its central URL changed did not restart an already running service. Installation now enables and restarts the worker. Uninstall no longer prunes unrelated Docker images.
- Starting the central script against an already running site could mistake the old site's health check for the new process and overwrite the PID file. The script now stops with a clear message when a site is already responding. Local health checks bypass proxy settings.
- Pre-check now warns that Python Code nodes may fail because this judge does not have the Python runner configured.
- Reopening a closed round now closes any other open round, so teams cannot be sent to the wrong problem. Non-finite marking values are rejected.
- Added a private practice account with unlimited runs and no public leaderboard/CSV entries.
- Corrected worker script names and the event database name in the README. Added the participant guide and event handbook.

## Remaining event risks

- No network egress filtering: uploaded workflows can reach the worker host and college network. Use dedicated machines and a network segment without sensitive systems; do not describe this as production isolation.
- Free AI-provider quotas and subjective rubric judgments may cause failures or score variability. Judge-side failures and exhausted retries are refunded; monitor incidents and preserve an appeal/manual-review option.
- Quick Cloudflare tunnel URLs change on restart. The event still depends on central power, network, DNS/tunnel availability, and enough worker slots.
- These two event packs use simulated Gmail, Telegram and Sheets; workflow model calls and grader calls use real APIs. They do not validate real OAuth accounts or deliver real messages.
- The worker installer changes system services and loads Docker images. A genuine college-PC install remains an on-site validation, not established by a dry run.
- Earlier automated commit security review failed with HTTP 429; see the local Claude security log. No complete replacement security scan has been performed.
