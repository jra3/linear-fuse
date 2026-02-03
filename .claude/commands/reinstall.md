# Reinstall LinearFS

Reinstall the linearfs binary while the service is running. Detect the platform and use the appropriate service manager.

## Steps

1. Detect the platform using `uname -s`
2. Stop the service:
   - **Linux**: `systemctl --user stop linearfs.service`
   - **macOS**: `launchctl stop com.linearfs.mount`
3. Build and install: `make install`
4. Start the service:
   - **Linux**: `systemctl --user start linearfs.service`
   - **macOS**: `launchctl start com.linearfs.mount`
5. Verify the service is running:
   - **Linux**: `systemctl --user status linearfs.service`
   - **macOS**: `launchctl list | grep linearfs`

Report success or any errors encountered.
