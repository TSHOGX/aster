# Terminals

[Documentation](../README.md) · User guide

The terminal dock provides a shell inside the Project. It shares files and the
execution environment with the Project's Agents. Terminal access requires the
corresponding permission and available compute resources.

## Start and return to a terminal

1. Open the terminal dock from the Project workspace heading.
2. Create a terminal or select an existing one.
3. Enter commands when the terminal shows that you control input.
4. Rename terminal tabs to distinguish long-running jobs.

Only one browser observer controls input at a time. If your view is read-only,
request the available input ownership using the terminal controls.

Closing a tab or hiding the dock detaches your view. The process keeps running.
Reopen it to reconnect. Some earlier output may no longer be retained; a missing
output notice does not mean the command failed. Save important output to a file
in the workspace rather than relying on terminal scrollback.

## Stop and restart

Use the terminal's Stop control when you intend to end its process. Ctrl+C can
interrupt a foreground command when the shell/program supports it. Reopening or
restarting an exited terminal begins a new process; it does not resume the old
shell state. Delete the terminal when you no longer need its retained record.

A connection failure is not proof that a process exited. Reconnect and inspect
status before starting the same command again. Project pause or administrative
restart can affect every terminal in that Project.

See [Files](files.md) for retrieving outputs and
[Troubleshooting](troubleshooting.md) for connection and permission failures.
