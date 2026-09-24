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
const NativeProtocol = 2
const NativeRPCPath = NativeRoot + "/" + PiPackage + "/dist/modes/rpc/rpc-mode.js"
const nativeRPCOriginal = "sha256:d0f92418f6aedaa4c64da68f65cb6f93e18dc65d13969eba45349d99f36dfca7"
const nativeRPCPatched = "sha256:ed1ca03f745037ace8323eac3f2fdfa8f93b31697f800a724f5c7a04c7f2d858"

const identityHooks = `    let session = runtimeHost.session;
    let activePrompt;
    // BatteryGPT native protocol 2: correlate exact Agent message objects with
    // product delivery tokens. The WeakMap is never model or Session content.
    const inputContext = new AsyncLocalStorage();
    const inputIdentity = new WeakMap();
    const installedAgents = new WeakSet();
    const installIdentityHooks = () => {
        if (installedAgents.has(session.agent)) return;
        installedAgents.add(session.agent);
        const prompt = session.agent.prompt.bind(session.agent);
        const steer = session.agent.steer.bind(session.agent);
        const processEvents = session.agent.processEvents.bind(session.agent);
        session.agent.prompt = (input, ...args) => {
            const token = inputContext.getStore();
            const message = Array.isArray(input) ? input.find((m) => m.role === "user") : input;
            if (token && message && typeof message === "object") inputIdentity.set(message, token);
            return prompt(input, ...args);
        };
        session.agent.steer = (message, ...args) => {
            const token = inputContext.getStore();
            if (token) inputIdentity.set(message, token);
            return steer(message, ...args);
        };
        session.agent.processEvents = (event) => {
            if (activePrompt && event.type === "message_start" && event.message.role === "user") {
                const token = inputIdentity.get(event.message);
                if (token) {
                    inputIdentity.delete(event.message);
                    activePrompt.inputTokens.delete(token);
                    output({ type: "input_consumed", input_token: token, run_id: activePrompt.id, occurred_at: new Date().toISOString() });
                }
            }
            return processEvents(event);
        };
    };`

const settledPrompt = `            case "prompt": {
                // BatteryGPT native protocol 2: agent_end is only a loop boundary.
                if (activePrompt) {
                    return error(id, "prompt", "A prompt is still settling");
                }
                const operation = { id, outcome: "completed", abortRequested: false, inputTokens: new Set() };
                activePrompt = operation;
                if (command.input_token) operation.inputTokens.add(command.input_token);
                const started = process.hrtime.bigint();
                let preflightSucceeded = false;
                const canceledBeforeDispatch = new Error("Prompt canceled before dispatch");
                const finish = (outcome, failure) => {
                    session.clearQueue();
                    output({ type: "inputs_reconciled", run_id: operation.id, removed_input_tokens: [...operation.inputTokens], complete: true, occurred_at: new Date().toISOString(), run_elapsed_ms: Number((process.hrtime.bigint() - started) / 1000000n) });
                    if (activePrompt === operation) activePrompt = undefined;
                    output({ type: "prompt_end", id, outcome, ...(failure ? { error: String(failure).slice(0, 1024) } : {}) });
                };
                void inputContext.run(command.input_token, () => session
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
                }))
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
		{"import * as crypto from \"node:crypto\";", "import * as crypto from \"node:crypto\";\nimport { AsyncLocalStorage } from \"node:async_hooks\";"},
		{"    let session = runtimeHost.session;\n    let unsubscribe;", identityHooks + "\n    installIdentityHooks();\n    let unsubscribe;"},
		{"        session = runtimeHost.session;\n        await session.bindExtensions({", "        session = runtimeHost.session;\n        installIdentityHooks();\n        await session.bindExtensions({"},
		{"        unsubscribe = session.subscribe((event) => {\n            output(event);\n        });", observePrompt},
		{"            case \"steer\": {\n                await session.steer(command.message, command.images);", "            case \"steer\": {\n                if (!activePrompt || activePrompt.id !== command.run_id || activePrompt.abortRequested) return error(id, \"steer\", \"Target run ended\");\n                if (command.input_token) activePrompt.inputTokens.add(command.input_token);\n                try {\n                    await inputContext.run(command.input_token, () => session.steer(command.message, command.images));\n                } catch (e) {\n                    activePrompt.inputTokens.delete(command.input_token);\n                    throw e;\n                }"},
		{"            case \"abort\": {\n                await session.abort();", "            case \"abort\": {\n                if (!activePrompt || activePrompt.id !== command.run_id) return error(id, \"abort\", \"Target run ended\");\n                activePrompt.abortRequested = true;\n                session.clearQueue();\n                session.abortCompaction();\n                await session.abort();"},
		{"//# sourceMappingURL=rpc-mode.js.map", "// BatteryGPT native settlement protocol 2; upstream source map does not apply."},
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
