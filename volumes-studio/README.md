# Volume Studio

Independent authoring and immutable distribution application.
The public entry is `/volumes-studio/`. Studio owns its own metadata and blobs;
Aster owns import, runtime materialization and project references. Central Aster
accounts remain the sole initial identity authority. Studio has application
sessions but no password store or account directory.

The backend is an independent Go module with no Aster business imports or engine
dependency. The [v1 contract](contracts/v1/README.md) and its independent consumer
decoder are implemented. The central identity adapter, independent gateway and
durable Studio sessions, authoring and runtime integration are implemented. See the [delivery tracker](../docs/volume-studio-delivery.md) and
[deployment instructions](deploy/README.md).

Run the independent checks from the repository root:

```sh
scripts/check.sh studio
scripts/check.sh studio-frontend
scripts/check.sh studio-browser
```
