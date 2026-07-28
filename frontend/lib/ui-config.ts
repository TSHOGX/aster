// Build-time UI toggles. Next inlines NEXT_PUBLIC_* into the client bundle at
// build time, so flipping one of these requires `npm run build` + a frontend
// restart — there is no runtime knob.

// Workspace upload entry points: the "Upload files" / "Upload folder" buttons in
// the artifact pane header and the drag-and-drop target that covers the pane.
// Hidden by default; set NEXT_PUBLIC_HARNESS_UPLOAD_UI=1 to bring them back.
// The backend upload route is unchanged — this only withholds the UI.
export const uploadUiEnabled = process.env.NEXT_PUBLIC_HARNESS_UPLOAD_UI === "1";
