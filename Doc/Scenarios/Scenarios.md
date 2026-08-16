# Everyday scenarios

How the service behaves in common situations. For installing and configuring the service see the [Setup guide](../Setup/Setup.md); for the project overview see the [README](../../README.md).

**Everything is working normally.** OBS is publishing to a source, viewers on every enabled platform fed by it see the live feed. Nothing special happens.

**You want to stream to only some platforms.** Use the Control tab: uncheck the platforms you want to skip (their streams end immediately), leave the rest checked. Re-check them later to bring them back live. Platforms are organized into groups with their own shared checkbox — uncheck a group to take every platform in it offline at once without touching their individual checkboxes, so re-enabling the group brings back exactly the ones that were on before.

**A source's content doesn't match what it's declared to carry** (e.g. it's declared as 2 audio tracks but OBS is only sending 1, or there's no video at all). The service catches this as soon as the source connects, before any platform goes live off it: you get an error toast, the platforms fed by that source are held back from starting (or, if they were already live off it, the broadcast is stopped), and their enable checkboxes are blocked until the mismatch is fixed. Fix the OBS-side output or the source's declared track count, then reconnect.

**Internet dropped / PC crashed / OBS closed unexpectedly (the default source).** Its platforms don't notice the drop — the backup video takes over on all of them instead of the live feed, and stays on until either:
- the connection comes back (a normal "Start Streaming" in OBS) — the backup stops, live video returns, viewers see a smooth cut with no visible pause (as soon as OBS sends a fresh keyframe — usually a couple of seconds);
- or `offline_timeout_sec` (30 minutes by default) passes with no recovery — the broadcast ends completely and you'll need to click "Start Streaming" in OBS again.

**An extra (non-default) source drops while the main broadcast is live.** Its platforms show their own backup video with the same seamless cut-away/cut-back mechanics as above, but there's no timeout tied to it specifically — they'll keep waiting for that source to reconnect for as long as the main broadcast stays live. If the main broadcast isn't live at the time (or you stop it around the same moment), that source's platforms just end cleanly instead — there'd be no live session left for a backup video to ever cut back from.

**No platform was reachable this broadcast** (typically a wrong key on the only enabled platform, or wrong keys on all of them). Looping the backup while retrying connections that never once succeeded wouldn't accomplish anything, so the service gives up on the first failed attempt: it skips the backup and the 30-minute wait, ends the broadcast, and (if the obs-source Browser Source has "Full access to OBS", [the OBS browser-source (Setup)](../Setup/Setup.md#6-add-the-browser-source)) tells OBS itself to stop too. You'll see an error toast, and the broadcast indicator turns into a red **FAILURE** badge. Note the "**no** platform" (across every source, not just one) — if at least one enabled platform connects, the broadcast proceeds and only the failed one drops (with a warning toast).

**One platform's key is wrong but others work.** That platform stops on its own (retrying an invalid key achieves nothing) with a warning toast naming it; every other platform keeps streaming, no FAILURE.

**A platform's connection drops after already working.** Different from a bad key: if a platform was working and *then* starts failing (a real network blip), the service keeps retrying it indefinitely, with the error toasts rate-limited so they don't spam you.

**You consciously click "Stop Streaming" in OBS.** A plain connection drop looks identical to the server whether it's a deliberate "Stop" or a lost connection. The obs-source Browser Source ([the OBS browser-source (Setup)](../Setup/Setup.md#6-add-the-browser-source)) watches OBS's main Stream output and signals the server — before OBS's RTMP connection drops — that this is a deliberate stop. The default source's platforms then end immediately, with no backup video and no timeout; any extra source's platforms that were already showing their backup end cleanly along with it, on the assumption there's no live session left for them to lean on. Without that Browser Source added, every "Stop" looks like an ordinary disconnect: backup video, then the timeout.

**You want to change settings (resolution, bitrate) mid-stream.** OBS won't let you change these fields while actively publishing — stop the stream (the "Stop" button, as above — no backup video shown), change the settings, and hit "Start" again. The service treats it as a fresh start.

If the settings change *during* a disconnect (the PC froze, you fixed the bitrate, then restarted OBS) — the service detects it and does a clean reconnect to every platform instead of an unsafe mid-connection swap. A short pause on recovery is normal in this case.

**You want to change a platform's URL/key, add a platform or source, move a platform between groups, or change the timeout/backup preset while already live.** All of these apply live from the Settings tab: editing a URL reconnects only that platform, adding a platform or source just adds it (enable it on Control when ready), and group/timeout/backup changes don't interrupt anything already streaming. Only the connect/read timeouts require a MediaMTX restart (which ends the broadcast — you'll be asked to confirm).

**The problem is between the VPS and a platform, not the streamer** (the server's own network, or an issue on the platform's end). The service detects the drop (or the "connection alive but stalled" case) and reconnects to that platform automatically, retrying until it succeeds. Viewers on that platform might see a brief "offline -> online again" (unavoidable — the connection genuinely re-establishes), but nothing needs to be done on the OBS side.

**The controller or the server itself got restarted** (an update, a crash, a VPS reboot) during an active broadcast. State isn't preserved across controller restarts — after restarting it comes up as "nothing is streaming". `install.sh` enables the `restreamd` unit, so a rebooted VPS brings the controller back on its own (`sudo systemctl status restreamd` to confirm); start streaming from OBS again.
