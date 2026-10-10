export const defaultDisplayName = "Aster";

// Keep the public environment lookup static so Next.js inlines the same value
// into server metadata and client components at build time.
export const displayName =
  process.env.NEXT_PUBLIC_PROJECT_BRAND_NAME?.trim() || defaultDisplayName;
export const displayInitial = Array.from(displayName)[0].toUpperCase();
