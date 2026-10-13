# Terms and lifecycle

[Documentation](../README.md) · Reference

| Term | Meaning |
| --- | --- |
| Project | A shared workspace and execution environment with its own access and resource allocation |
| Conversation / Session | An Agent conversation within a Project; other conversations run independently |
| Prompt | A submitted input that can wait, be held or be consumed by a run |
| Run | An execution of Agent work; submitting a prompt does not mean the run completed |
| Terminal | A retained shell process; closing the browser view only detaches it |
| Schedule | Saved instructions and a trigger for future Project work |
| Environment template | A selectable runtime/storage configuration for creating Projects |
| Runtime release | An immutable sandbox image version registered by an operator |
| Volume | Persistent file storage, either owned by a Project or referenced as shared content |
| Publication | An immutable reviewed file-tree version |
| Shared directory | Mutable storage available to separately authorized users |
| Environment update | An explicit deployment of compatible immutable data versions to existing Projects |
| Owner service | The single authoritative Aster application process for an installation |
| Gateway | The separately supervised service forwarding authorized model requests |
| Volume Studio | The independent library authoring and publication application |

## Project lifecycle

A Project saves its chosen environment, resource sizes and mounts. File upload may
initialize storage before compute starts. Starting an Agent or terminal requires
usable runtime artifacts, current permissions and available compute quota.

When idle work settles, compute can pause while files and history remain available.
Resume uses the saved runtime and owned data. Changing a default does not rebuild
the Project. Compatible data publications and explicit network changes can apply
at a start/resume boundary through their own controls.

Administrative controls or suspension can leave an execution hold. Restoring login
is not the same as resuming a held Project. Deletion removes owned resources only
after cleanup completes; shared sources can remain for other Projects.

## Read status literally

- **Accepted** means the action was recorded, not that remote work finished.
- **Queued/held** describes a prompt waiting for execution.
- **Paused** describes retained work or compute that is not currently running.
- **Waiting for update** can mean the Project has not reached its next start/resume.
- **Unknown** means the result cannot yet be established. It is not a failed/no-effect result.
- **Ready** in volume inventory means a local artifact is usable; it does not reserve resources.
- **Exhausted** on a one-shot means its trigger was consumed; inspect run history for its result.

See [Project controls](../admin/projects.md) and
[troubleshooting](../users/troubleshooting.md) before retrying unknown work.
