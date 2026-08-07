// Build-time UI toggles. Next inlines NEXT_PUBLIC_* into the client bundle at
// build time, so flipping one of these requires `npm run build` + a frontend
// restart — there is no runtime knob.

// Workspace upload entry points: the "Upload files" / "Upload folder" buttons in
// the artifact pane header and the drag-and-drop target that covers the pane.
// Hidden by default; set NEXT_PUBLIC_HARNESS_UPLOAD_UI=1 to bring them back.
// The backend upload route is unchanged — this only withholds the UI.
export const uploadUiEnabled = process.env.NEXT_PUBLIC_HARNESS_UPLOAD_UI === "1";

/**
 * The product name, shown in the wordmark, the page title, and as the author of
 * every assistant reply.
 *
 * Configurable via NEXT_PUBLIC_HARNESS_BRAND_NAME so a deployment can be
 * rebranded without a code change. Deliberately never translated: a brand name
 * is the same in every locale, and the reply byline has to match the wordmark in
 * the corner or the two read as different products.
 *
 * The `||` (not `??`) matters — an unset NEXT_PUBLIC_* variable inlines as the
 * empty string in some build configurations, not undefined, and an empty
 * wordmark is worse than the default.
 */
export const brandName = process.env.NEXT_PUBLIC_HARNESS_BRAND_NAME || "BatteryGPT";
