# Studio frontend

Independent Next.js App Router build at `/volumes-studio/`. The fixed `basePath`
and trailing-slash policy match the front door. Browser API calls always include
that prefix. No Aster project provider, filesystem path or engine state is shared.

The editor supports libraries, directories, small UTF-8 files, rename/delete,
resumable file/folder uploads, draft changes, restore, publication review and
version history. An open text buffer retains its original expected revision;
polling newer metadata never silently authorizes an overwrite. Publication review
also pins the revision being reviewed. Uploads hash in a worker using bounded
buffers, then transfer sequential chunks; selecting the same file resumes an
unfinished server session. Folder uploads report individual failures.

Source content is always served as a download with `nosniff` and CSP sandbox.
Text is decoded as inert editor data. Unsupported/binary files use download.
The application never renders uploaded HTML as a same-origin document.

```sh
npm ci
npm run lint
npm test
npm run build
npm run typecheck
```

From a full repository checkout, `npm run test:browser` builds disposable Go
processes and exercises the packaged frontend against the real central account
adapter, Studio gateway/backend and TLS front door. It requires the Aster Go
module dependencies, OpenSSL and a Playwright Chromium installation. It never
uses a live database, engine, account or deployment.
