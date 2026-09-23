package runtimerelease

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// NativeProtocol identifies the pinned Pi RPC extension that reports settlement
// of the complete prompt Promise, including retry and compaction continuations.
const NativeProtocol = 1
const NativeRPCPath = NativeRoot + "/" + PiPackage + "/dist/modes/rpc/rpc-mode.js"
const nativeRPCOriginal = "sha256:d0f92418f6aedaa4c64da68f65cb6f93e18dc65d13969eba45349d99f36dfca7"
const nativeRPCPatched = "sha256:b6e5ebd07444436875ee73a168b100f72195aceac4817781a7cab5feac17a24a"

const settledPrompt = `            case "prompt": {
                // BatteryGPT native protocol 1: agent_end is only a loop boundary.
                if (activePrompt) {
                    return error(id, "prompt", "A prompt is still settling");
                }
                const operation = { id, outcome: "completed", abortRequested: false };
                activePrompt = operation;
                let preflightSucceeded = false;
                const canceledBeforeDispatch = new Error("Prompt canceled before dispatch");
                const finish = (outcome, failure) => {
                    if (activePrompt === operation) activePrompt = undefined;
                    output({ type: "prompt_end", id, outcome, ...(failure ? { error: String(failure).slice(0, 1024) } : {}) });
                };
                void session
                    .prompt(command.message, {
                    images: command.images,
                    streamingBehavior: command.streamingBehavior,
                    source: "rpc",
                    preflightResult: (didSucceed) => {
                        if (didSucceed) {
                            preflightSucceeded = true;
                            output(success(id, "prompt"));
                            if (operation.abortRequested) throw canceledBeforeDispatch;
                        }
                    },
                })
                    .then(() => { if (preflightSucceeded) finish(operation.outcome); })
                    .catch((e) => {
                    if (!preflightSucceeded) {
                        if (activePrompt === operation) activePrompt = undefined;
                        output(error(id, "prompt", e.message));
                    } else {
                        finish(e === canceledBeforeDispatch ? "canceled" : "failed", e.message);
                    }
                });
                return undefined;
            }
`

const observePrompt = `        unsubscribe = session.subscribe((event) => {
            if (activePrompt) {
                if (activePrompt.abortRequested && event.type === "auto_retry_start") {
                    queueMicrotask(() => session.abortRetry());
                }
                if (activePrompt.abortRequested && event.type === "compaction_start") {
                    queueMicrotask(() => session.abortCompaction());
                }
                if (event.type === "message_end" && event.message.role === "assistant") {
                    activePrompt.outcome = event.message.stopReason === "aborted" ? "canceled"
                        : event.message.stopReason === "error" ? "failed" : "completed";
                }
                if (event.type === "auto_retry_end" && !event.success && event.finalError === "Retry cancelled") {
                    activePrompt.outcome = "canceled";
                }
                if (event.type === "compaction_end" && event.aborted) {
                    activePrompt.outcome = "canceled";
                }
            }
            output(event);
        });`

func patchNativeRPC(raw []byte) ([]byte, error) {
	digest := fmt.Sprintf("sha256:%x", sha256.Sum256(raw))
	if digest == nativeRPCPatched {
		return raw, nil
	}
	if digest != nativeRPCOriginal {
		return nil, errors.New("Pi RPC source differs from the pinned 0.77.0 input")
	}
	source := string(raw)
	start := strings.Index(source, "            case \"prompt\": {")
	end := strings.Index(source, "            case \"steer\":")
	if start < 0 || end <= start {
		return nil, errors.New("Pi prompt RPC patch boundary missing")
	}
	source = source[:start] + settledPrompt + source[end:]
	for _, replacement := range [][2]string{
		{"    let session = runtimeHost.session;", "    let session = runtimeHost.session;\n    let activePrompt;"},
		{"        unsubscribe = session.subscribe((event) => {\n            output(event);\n        });", observePrompt},
		{"            case \"abort\": {\n                await session.abort();", "            case \"abort\": {\n                if (activePrompt) activePrompt.abortRequested = true;\n                session.abortCompaction();\n                await session.abort();"},
		{"//# sourceMappingURL=rpc-mode.js.map", "// BatteryGPT native settlement protocol 1; upstream source map does not apply."},
	} {
		if strings.Count(source, replacement[0]) != 1 {
			return nil, errors.New("Pi RPC patch marker differs from pinned source")
		}
		source = strings.Replace(source, replacement[0], replacement[1], 1)
	}
	if digest = fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(source))); digest != nativeRPCPatched {
		return nil, fmt.Errorf("Pi RPC patch output differs from pin: %s", digest)
	}
	return []byte(source), nil
}

// PatchNativeRPC is used only while assembling a fresh immutable image. Both the
// upstream input and extended output are pinned; unknown source is never patched.
func PatchNativeRPC(root string) error {
	root, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	if root == "/" {
		return errors.New("native RPC patch requires a separate build root")
	}
	if _, err = os.Stat(filepath.Join(root, strings.TrimPrefix(ManifestPath, "/"))); !os.IsNotExist(err) {
		return errors.New("native RPC patch refuses an existing runtime release")
	}
	file, err := resolve(root, NativeRPCPath)
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	patched, err := patchNativeRPC(raw)
	if err != nil {
		return err
	}
	return os.WriteFile(file, patched, 0644)
}

func verifyNativeRPC(root string) error {
	file, err := resolve(root, NativeRPCPath)
	if err != nil {
		return err
	}
	digest, err := hashFile(file)
	if err != nil {
		return err
	}
	if digest != nativeRPCPatched {
		return errors.New("native RPC settlement protocol is absent or modified")
	}
	return nil
}
