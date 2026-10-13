# File preview support and limits

[Documentation](../README.md) · Reference

A preview is a convenience view of the original file. Download the original when
layout fidelity matters or a preview is unavailable. Opening a preview does not
start paused compute or modify the source.

## Aster Project files

| Format | Available view | Important limits |
| --- | --- | --- |
| Text and supported code, XML, YAML | Read-only Source with find, line navigation and copy | Ordinary preview input up to 20 MiB; explicit text editing up to 1 MiB |
| Markdown and JSON | Formatted view or Source | Above 256 KiB, opens as labeled Source |
| HTML | Restricted rendered preview or Source | Scripts, forms and external network content are blocked |
| CSV/TSV | Table preview | Subject to ordinary preview and content limits |
| PNG/JPEG/static GIF | Image preview | Subject to ordinary preview and image limits |
| PDF | Native browser PDF view | Depends on browser support |
| DOCX | Formatted pages, tables and embedded images | Office input up to 100 MiB, with separate complexity limits |
| XLSX | Read-only spreadsheet with sheet tabs and selection/copy | Some chart/drawing fidelity is unavailable |
| PPTX | Navigable slides and keyboard arrows | Rendering may differ from the original application |
| Legacy XLS, SVG and unsupported formats | Download | No built-in visual preview |

Ordinary local preview input is limited to 20 MiB; Office input is limited to
100 MiB. These are preview bounds, not file-transfer entitlements. Supported source
text uses strict UTF-8 or BOM-marked UTF-16 decoding. Edit text is offered only for
supported nonbinary text within the 1 MiB editing limit.

## Why a smaller Office file can still be refused

Compressed file size does not measure rendering cost. Office previews also enforce:

- Up to 4,096 ZIP entries, 64 MiB per entry and 256 MiB expanded content.
- XML depth up to 64 and up to four million nodes.
- Spreadsheet limits including one million populated cells and a 384 MiB estimated
  working set, plus coordinate and image limits.

A sparse distant cell is not rejected simply for being beyond row 2,000. Encrypted,
malformed, unsupported or excessively complex documents fall back to Download.
These limits do not promise faithful rendering or a fixed browser memory footprint.

Inspection can take up to 60 seconds, viewer startup 30 seconds and document
preparation 180 seconds. The loading display identifies the phase and provides
Cancel/Download; a long wait does not imply a larger limit. Retry a transient viewer
failure, or use the original application if it persists. If Safari fails to start
a viewer, another browser or downloading the source may help.

## Volume Studio previews

Studio has a separate authoring preview set: UTF-8 text/Markdown up to 1 MiB and
PNG/JPEG/GIF/WebP images up to 16 MiB. PDF, SVG, Office and other binary formats
use authenticated Download. Do not infer Studio support from the Project-file table.
See [Studio authoring](../admin/volume-studio.md).

## Security and fidelity

HTML previews do not execute scripts or fetch remote resources. Office previews
run in a restricted browser frame without application credentials or storage access;
the frame is not a guarantee of bounded CPU or memory usage. Preview restrictions
can omit active content or external resources. Source files are not rewritten to
make them render.

The shipped Office viewers use docx-preview, @js-preview/excel and pptx-preview.
See [third-party notices](../third-party-notices.md) for redistribution notices.
