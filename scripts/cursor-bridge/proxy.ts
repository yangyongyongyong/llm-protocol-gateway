/**
 * Local OpenAI-compatible proxy that translates requests to Cursor's gRPC protocol.
 *
 * Accepts POST /v1/chat/completions in OpenAI format, translates to Cursor's
 * protobuf/HTTP2 Connect protocol, and streams back OpenAI-format SSE.
 *
 * Tool calling uses Cursor's native MCP tool protocol:
 * - OpenAI tool defs → McpToolDefinition in RequestContext
 * - Cursor toolCallStarted/Delta/Completed → OpenAI tool_calls SSE chunks
 * - mcpArgs exec → pause stream, return tool_calls to caller
 * - Follow-up request with tool results → resume bridge with mcpResult
 *
 * HTTP/2 transport is delegated to a Node child process (h2-bridge.mjs)
 * because Bun's node:http2 module is broken.
 */
import { create, fromBinary, fromJson, type JsonValue, toBinary, toJson } from "@bufbuild/protobuf";
import { ValueSchema } from "@bufbuild/protobuf/wkt";
import {
  AgentClientMessageSchema,
  AgentRunRequestSchema,
  AgentServerMessageSchema,
  ClientHeartbeatSchema,
  ConversationActionSchema,
  ConversationStateStructureSchema,
  ConversationStepSchema,
  AgentConversationTurnStructureSchema,
  ConversationTurnStructureSchema,
  AssistantMessageSchema,
  BackgroundShellSpawnResultSchema,
  DeleteResultSchema,
  DeleteRejectedSchema,
  DiagnosticsResultSchema,
  ExecClientControlMessageSchema,
  ExecClientMessageSchema,
  ExecClientStreamCloseSchema,
  FetchErrorSchema,
  FetchResultSchema,
  GetBlobResultSchema,
  GrepErrorSchema,
  GrepResultSchema,
  KvClientMessageSchema,
  LsRejectedSchema,
  LsResultSchema,
  McpErrorSchema,
  McpResultSchema,
  McpSuccessSchema,
  McpTextContentSchema,
  McpToolDefinitionSchema,
  McpToolResultContentItemSchema,
  ModelDetailsSchema,
  ReadRejectedSchema,
  ReadResultSchema,
  RequestContextResultSchema,
  RequestContextSchema,
  RequestContextSuccessSchema,
  SetBlobResultSchema,
  ShellRejectedSchema,
  ShellResultSchema,
  ShellSuccessSchema,
  ShellStreamSchema,
  ShellStreamStdoutSchema,
  ShellStreamStderrSchema,
  ShellStreamExitSchema,
  ShellStreamStartSchema,
  UserMessageActionSchema,
  UserMessageSchema,
  SelectedContextSchema,
  SelectedImageSchema,
  SelectedImage_BlobIdWithDataSchema,
  SelectedImage_DimensionSchema,
  WriteRejectedSchema,
  WriteResultSchema,
  WriteShellStdinErrorSchema,
  WriteShellStdinResultSchema,
  type AgentServerMessage,
  type ConversationStateStructure,
  type ExecServerMessage,
  type KvServerMessage,
  type McpToolDefinition,
  type SelectedImage,
} from "./proto/agent_pb";
import { createHash } from "node:crypto";
import { spawnSync } from "node:child_process";
import { appendFileSync } from "node:fs";
import { resolve as pathResolve } from "node:path";
import { resolveCursorModelId } from "./models";

const CURSOR_API_URL = process.env.CURSOR_API_URL ?? "https://api2.cursor.sh";
const CONNECT_END_STREAM_FLAG = 0b00000010;
const BRIDGE_PATH = pathResolve(import.meta.dir, "h2-bridge.mjs");
const SSE_HEADERS = {
  "Content-Type": "text/event-stream",
  "Cache-Control": "no-cache",
  Connection: "keep-alive",
} as const;

interface OpenAIToolCall {
  id: string;
  type: "function";
  function: { name: string; arguments: string };
}

/** A single element in an OpenAI multi-part content array. */
interface ContentPart {
  type: string;
  text?: string;
  image_url?: string | { url?: string; detail?: string };
}

interface ParsedImagePart {
  mimeType: string;
  data: Uint8Array;
}

interface OpenAIMessage {
  role: "system" | "user" | "assistant" | "tool";
  content: string | null | ContentPart[];
  tool_call_id?: string;
  tool_calls?: OpenAIToolCall[];
}

interface OpenAIToolDef {
  type?: string;
  name?: string;
  description?: string;
  parameters?: Record<string, unknown>;
  function?: {
    name: string;
    description?: string;
    parameters?: Record<string, unknown>;
  };
}

interface NormalizedFunctionTool {
  name: string;
  description?: string;
  parameters?: Record<string, unknown>;
}

interface ChatCompletionRequest {
  model: string;
  messages: OpenAIMessage[];
  stream?: boolean;
  temperature?: number;
  max_tokens?: number;
  tools?: OpenAIToolDef[] | unknown[];
  tool_choice?: unknown;
}

interface ImageGenerationRequest {
  model?: string;
  prompt: string;
  n?: number;
  size?: string;
  response_format?: "url" | "b64_json";
}


interface CursorRequestPayload {
  requestBytes: Uint8Array;
  blobStore: Map<string, Uint8Array>;
  mcpTools: McpToolDefinition[];
}

/** A pending tool execution waiting for results from the caller. */
interface PendingExec {
  execId: string;
  execMsgId: number;
  /** Sanitized OpenAI-facing call id (no newlines / dual ids). */
  toolCallId: string;
  toolName: string;
  /** Decoded arguments JSON string for SSE tool_calls emission. */
  decodedArgs: string;
  /**
   * How to resume Cursor after the client returns a tool result.
   * - mcp: send mcpResult (default)
   * - shell: Cursor used native shellArgs; send shellResult success
   * - shellStream: Cursor used shellStreamArgs; send shellStream stdout+exit
   */
  resumeKind?: "mcp" | "shell" | "shellStream";
  shellCommand?: string;
  shellCwd?: string;
}

/**
 * Cursor sometimes returns toolCallId as two ids joined by a newline, e.g.
 * `call_xxx\nfc_yyy`. OpenAI/Codex require a single call_id; newlines also
 * break tool-result matching and can confuse Responses clients into hanging.
 */
function sanitizeToolCallId(raw: string | undefined | null): string {
  const parts = String(raw ?? "")
    .replace(/\r/g, "")
    .split("\n")
    .map((part) => part.trim())
    .filter(Boolean);
  const preferred =
    parts.find((part) => part.startsWith("call_")) ??
    parts.find((part) => /^[A-Za-z0-9_-]{8,}$/.test(part)) ??
    parts[0];
  if (preferred) return preferred;
  return `call_${crypto.randomUUID().replace(/-/g, "")}`;
}

/** Prefer Codex/OpenAI shell tool names when remapping Cursor native shellArgs. */
function findClientShellToolName(mcpTools: McpToolDefinition[]): string | null {
  const preferred = ["exec_command", "shell", "bash", "run_terminal_cmd"];
  for (const name of preferred) {
    if (mcpTools.some((tool) => tool.name === name || tool.toolName === name)) {
      return name;
    }
  }
  return null;
}

function buildShellToolArgs(toolName: string, command: string, cwd: string): string {
  if (toolName === "exec_command") {
    return JSON.stringify({ cmd: command });
  }
  const payload: Record<string, string> = { command };
  if (cwd) payload.working_directory = cwd;
  return JSON.stringify(payload);
}

/** A bridge kept alive across requests for tool result continuation. */
interface ActiveBridge {
  bridge: ReturnType<typeof spawnBridge>;
  heartbeatTimer: NodeJS.Timeout;
  blobStore: Map<string, Uint8Array>;
  mcpTools: McpToolDefinition[];
  pendingExecs: PendingExec[];
}

// Active bridges keyed by a session token (derived from conversation state).
// When tool_calls are returned, the bridge stays alive. The next request
// with tool results looks up the bridge and sends mcpResult messages.
const activeBridges = new Map<string, ActiveBridge>();

interface StoredConversation {
  conversationId: string;
  checkpoint: Uint8Array | null;
  blobStore: Map<string, Uint8Array>;
  lastAccessMs: number;
}

const conversationStates = new Map<string, StoredConversation>();
const CONVERSATION_TTL_MS = 30 * 60 * 1000; // 30 minutes

function evictStaleConversations(): void {
  const now = Date.now();
  for (const [key, stored] of conversationStates) {
    if (now - stored.lastAccessMs > CONVERSATION_TTL_MS) {
      conversationStates.delete(key);
    }
  }
}

/** Length-prefix a message: [4-byte BE length][payload] */
function lpEncode(data: Uint8Array): Buffer {
  const buf = Buffer.alloc(4 + data.length);
  buf.writeUInt32BE(data.length, 0);
  buf.set(data, 4);
  return buf;
}

/** Connect protocol frame: [1-byte flags][4-byte BE length][payload] */
function frameConnectMessage(data: Uint8Array, flags = 0): Buffer {
  const frame = Buffer.alloc(5 + data.length);
  frame[0] = flags;
  frame.writeUInt32BE(data.length, 1);
  frame.set(data, 5);
  return frame;
}

/**
 * Spawn the Node H2 bridge and return read/write handles.
 * The bridge uses length-prefixed framing on stdin/stdout.
 */
interface SpawnBridgeOptions {
  accessToken: string;
  rpcPath: string;
  url?: string;
  /** "connect" (default) or "proto" for unary application/proto RPCs. */
  transport?: "connect" | "proto";
  contentType?: string;
  clientType?: string;
  clientVersion?: string;
}

function spawnBridge(options: SpawnBridgeOptions): {
  proc: ReturnType<typeof Bun.spawn>;
  write: (data: Uint8Array) => void;
  end: () => void;
  onData: (cb: (chunk: Buffer) => void) => void;
  onClose: (cb: (code: number) => void) => void;
  /** True while the bridge subprocess is still running. */
  get alive(): boolean;
} {
  const proc = Bun.spawn(["node", BRIDGE_PATH], {
    stdin: "pipe",
    stdout: "pipe",
    stderr: "ignore",
  });

  const config = JSON.stringify({
    accessToken: options.accessToken,
    url: options.url ?? CURSOR_API_URL,
    path: options.rpcPath,
    transport: options.transport ?? "connect",
    contentType: options.contentType,
    clientType: options.clientType,
    clientVersion: options.clientVersion,
  });
  proc.stdin.write(lpEncode(new TextEncoder().encode(config)));

  const cbs = {
    data: null as ((chunk: Buffer) => void) | null,
    close: null as ((code: number) => void) | null,
  };

  // Track exit state so late onClose registrations fire immediately.
  let exited = false;
  let exitCode = 1;

  (async () => {
    const reader = proc.stdout.getReader();
    let pending = Buffer.alloc(0);

    try {
      while (true) {
        const { done, value } = await reader.read();
        if (done) break;
        pending = Buffer.concat([pending, Buffer.from(value)]);

        while (pending.length >= 4) {
          const len = pending.readUInt32BE(0);
          if (pending.length < 4 + len) break;
          const payload = pending.subarray(4, 4 + len);
          pending = pending.subarray(4 + len);
          cbs.data?.(Buffer.from(payload));
        }
      }
    } catch {
      // Stream ended
    }

    const code = await proc.exited ?? 1;
    exited = true;
    exitCode = code;
    cbs.close?.(code);
  })();

  return {
    proc,
    get alive() { return !exited; },
    write(data) {
      try { proc.stdin.write(lpEncode(data)); } catch {}
    },
    end() {
      try {
        proc.stdin.write(lpEncode(new Uint8Array(0)));
        proc.stdin.end();
      } catch {}
    },
    onData(cb) { cbs.data = cb; },
    onClose(cb) {
      if (exited) {
        // Process already exited — invoke immediately so streams don't hang.
        queueMicrotask(() => cb(exitCode));
      } else {
        cbs.close = cb;
      }
    },
  };
}

export interface CursorUnaryRpcOptions {
  accessToken: string;
  rpcPath: string;
  requestBody: Uint8Array;
  url?: string;
  timeoutMs?: number;
  /** "connect" (default) or "proto" for Cursor unary application/proto. */
  transport?: "connect" | "proto";
  contentType?: string;
  clientType?: string;
  clientVersion?: string;
}

export async function callCursorUnaryRpc(
  options: CursorUnaryRpcOptions,
 ): Promise<{ body: Uint8Array; exitCode: number; timedOut: boolean }> {
  const transport = options.transport ?? "connect";
  const bridge = spawnBridge({
    accessToken: options.accessToken,
    rpcPath: options.rpcPath,
    url: options.url,
    transport,
    contentType: options.contentType,
    clientType: options.clientType,
    clientVersion: options.clientVersion,
  });
  const chunks: Buffer[] = [];
  const { promise, resolve } = Promise.withResolvers<{
    body: Uint8Array;
    exitCode: number;
    timedOut: boolean;
  }>();
  let timedOut = false;
  const timeoutMs = options.timeoutMs ?? 5_000;
  const timeout = timeoutMs > 0
    ? setTimeout(() => {
        timedOut = true;
        try { bridge.proc.kill(); } catch {}
      }, timeoutMs)
    : undefined;

  bridge.onData((chunk) => {
    chunks.push(Buffer.from(chunk));
  });
  bridge.onClose((exitCode) => {
    if (timeout) clearTimeout(timeout);
    resolve({
      body: Buffer.concat(chunks),
      exitCode,
      timedOut,
    });
  });

  // Connect unary wraps the protobuf payload; application/proto sends raw bytes.
  const wireBody =
    transport === "proto"
      ? Buffer.from(options.requestBody)
      : frameConnectMessage(options.requestBody);
  bridge.write(wireBody);
  bridge.end();

  return promise;
}

let proxyServer: ReturnType<typeof Bun.serve> | undefined;
let proxyPort: number | undefined;
let proxyAccessTokenProvider: (() => Promise<string>) | undefined;
let proxyModels: Array<{ id: string; name: string }> = [];

function buildOpenAIModelList(models: ReadonlyArray<{ id: string; name: string }>): Array<{
  id: string;
  object: "model";
  created: number;
  owned_by: string;
}> {
  return models.map((model) => ({
    id: model.id,
    object: "model",
    created: 0,
    owned_by: "cursor",
  }));
}

export function getProxyPort(): number | undefined {
  return proxyPort;
}

export async function startProxy(
  getAccessToken: () => Promise<string>,
  models: ReadonlyArray<{ id: string; name: string }> = [],
): Promise<number> {
  proxyAccessTokenProvider = getAccessToken;
  proxyModels = models.map((model) => ({
    id: model.id,
    name: model.name,
  }));
  if (proxyServer && proxyPort) return proxyPort;

  proxyServer = Bun.serve({
    port: 0,
    idleTimeout: 255, // max — Cursor responses can take 30s+
    async fetch(req) {
      const url = new URL(req.url);

      if (req.method === "GET" && url.pathname === "/v1/models") {
        // Refresh when the in-memory catalog TTL expires (or ?refresh=1).
        // Avoids a full Cursor RPC on every UI poll while still picking up
        // newly shipped models within a few minutes / on provider test.
        try {
          if (proxyAccessTokenProvider) {
            const { getCursorModels } = await import("./models.ts");
            const token = await proxyAccessTokenProvider();
            const force = url.searchParams.get("refresh") === "1";
            const fresh = await getCursorModels(token, { force });
            proxyModels = fresh.map((model) => ({ id: model.id, name: model.name }));
          }
        } catch (err) {
          console.warn("[cursor-bridge] refresh /v1/models failed:", err);
        }
        return new Response(
          JSON.stringify({
            object: "list",
            data: buildOpenAIModelList(proxyModels),
          }),
          { headers: { "Content-Type": "application/json" } },
        );
      }

      if (req.method === "POST" && url.pathname === "/v1/chat/completions") {
        try {
          const body = (await req.json()) as ChatCompletionRequest;
          if (!proxyAccessTokenProvider) {
            throw new Error("Cursor proxy access token provider not configured");
          }
          const accessToken = await proxyAccessTokenProvider();
          return await handleChatCompletion(body, accessToken);
        } catch (err) {
          const message = err instanceof Error ? err.message : String(err);
          return new Response(
            JSON.stringify({
              error: { message, type: "server_error", code: "internal_error" },
            }),
            { status: 500, headers: { "Content-Type": "application/json" } },
          );
        }
      }

      if (req.method === "POST" && url.pathname === "/v1/images/generations") {
        try {
          const body = (await req.json()) as ImageGenerationRequest;
          if (!proxyAccessTokenProvider) {
            throw new Error("Cursor proxy access token provider not configured");
          }
          const accessToken = await proxyAccessTokenProvider();
          return handleImageGenerations(body, accessToken);
        } catch (err) {
          const message = err instanceof Error ? err.message : String(err);
          return new Response(
            JSON.stringify({
              error: { message, type: "server_error", code: "internal_error" },
            }),
            { status: 500, headers: { "Content-Type": "application/json" } },
          );
        }
      }

      return new Response("Not Found", { status: 404 });
    },
  });

  proxyPort = proxyServer.port;
  if (!proxyPort) throw new Error("Failed to bind proxy to a port");
  return proxyPort;
}

export function stopProxy(): void {
  if (proxyServer) {
    proxyServer.stop();
    proxyServer = undefined;
    proxyPort = undefined;
    proxyAccessTokenProvider = undefined;
    proxyModels = [];
  }
  // Clean up any lingering bridges
  for (const active of activeBridges.values()) {
    clearInterval(active.heartbeatTimer);
    active.bridge.end();
  }
  activeBridges.clear();
  conversationStates.clear();
}

function handleChatCompletion(
  body: ChatCompletionRequest,
  accessToken: string,
): Response | Promise<Response> {
  return handleChatCompletionAsync(body, accessToken);
}

async function handleChatCompletionAsync(
  body: ChatCompletionRequest,
  accessToken: string,
): Promise<Response> {
  const { systemPrompt, userText, turns, toolResults } = parseMessages(body.messages);
  const requestedModelId = body.model;
  const modelId = resolveCursorModelId(requestedModelId, proxyModels);
  const tools = collectChatTools(body.tools);
  const userImages = await extractImagesFromLastUserMessage(body.messages);
  const bridgeKey = deriveBridgeKey(modelId, body.messages);
  const debugLine = `[${new Date().toISOString()}] key=${bridgeKey} requested=${requestedModelId} model=${modelId} user=${JSON.stringify(userText).slice(0, 80)} tools=${tools.length} toolResults=${toolResults.length} ids=${JSON.stringify(toolResults.map((r) => r.toolCallId))} active=${activeBridges.has(bridgeKey)} images=${userImages.length}\n`;
  try {
    appendFileSync("/tmp/cursor-bridge-debug.log", debugLine);
  } catch {}

  if (!userText && toolResults.length === 0 && userImages.length === 0) {
    return new Response(
      JSON.stringify({
        error: {
          message: "No user message found",
          type: "invalid_request_error",
        },
      }),
      { status: 400, headers: { "Content-Type": "application/json" } },
    );
  }

  // Check for an active bridge waiting for tool results
  const activeBridge = activeBridges.get(bridgeKey);

  if (activeBridge && toolResults.length > 0) {
    activeBridges.delete(bridgeKey);

    if (activeBridge.bridge.alive) {
      // Resume the live bridge with tool results
      return handleToolResultResume(activeBridge, toolResults, modelId, bridgeKey);
    }

    // Bridge died (timeout, server disconnect, etc.).
    // Clean up and fall through to start a fresh bridge.
    clearInterval(activeBridge.heartbeatTimer);
    activeBridge.bridge.end();
  }

  // Clean up stale bridge if present
  if (activeBridge && activeBridges.has(bridgeKey)) {
    clearInterval(activeBridge.heartbeatTimer);
    activeBridge.bridge.end();
    activeBridges.delete(bridgeKey);
  }

  let stored = conversationStates.get(bridgeKey);
  if (!stored) {
    stored = {
      conversationId: crypto.randomUUID(),
      checkpoint: null,
      blobStore: new Map(),
      lastAccessMs: Date.now(),
    };
    conversationStates.set(bridgeKey, stored);
  }
  stored.lastAccessMs = Date.now();
  evictStaleConversations();

  // Build the request. When tool results are present but the bridge died,
  // we must still include the last user text so Cursor has context.
  const mcpTools = buildMcpToolDefinitions(tools);
  const effectiveUserText = userText
    || (userImages.length > 0 ? "请结合我附带的图片回答。" : "")
    || (toolResults.length > 0 ? toolResults.map((r) => r.content).join("\n") : "");
  const imageBlobStore = new Map<string, Uint8Array>();
  const selectedImages = userImages.map((image) => buildSelectedImage(image, imageBlobStore));
  const payload = buildCursorRequest(
    modelId, systemPrompt, effectiveUserText, turns,
    stored.conversationId, stored.checkpoint, stored.blobStore,
    selectedImages,
  );
  for (const [key, value] of imageBlobStore) {
    payload.blobStore.set(key, value);
  }
  payload.mcpTools = mcpTools;

  if (body.stream === false) {
    return handleNonStreamingResponse(payload, accessToken, modelId, bridgeKey);
  }
  return handleStreamingResponse(payload, accessToken, modelId, bridgeKey);
}

function openAIErrorPayload(message: string, code?: string): object {
  const normalized = (code ?? "").toLowerCase();
  const isNotFound = normalized === "not_found" || /not_found/i.test(message);
  return {
    error: {
      message,
      type: isNotFound ? "invalid_request_error" : "server_error",
      code: isNotFound ? "model_not_found" : (code || "upstream_error"),
    },
  };
}

function connectErrorHttpStatus(code?: string, message?: string): number {
  const normalized = (code ?? "").toLowerCase();
  if (normalized === "not_found" || /not_found/i.test(message ?? "")) {
    return 404;
  }
  return 502;
}

interface ToolResultInfo {
  toolCallId: string;
  content: string;
}

interface ParsedMessages {
  systemPrompt: string;
  userText: string;
  turns: Array<{ userText: string; assistantText: string }>;
  toolResults: ToolResultInfo[];
}

/** Normalize OpenAI message content to a plain string. */
function textContent(content: OpenAIMessage["content"]): string {
  if (content == null) return "";
  if (typeof content === "string") return content;
  return content
    .filter((p) => p.type === "text" || p.type === "input_text")
    .map((p) => p.text ?? "")
    .filter(Boolean)
    .join("\n");
}

function imageURLFromPart(part: ContentPart): string {
  if (part.type === "input_image" && typeof (part as { image_url?: string }).image_url === "string") {
    return String((part as { image_url?: string }).image_url ?? "").trim();
  }
  if (part.image_url == null) return "";
  if (typeof part.image_url === "string") return part.image_url.trim();
  return String(part.image_url.url ?? "").trim();
}

function parseDataURL(url: string): ParsedImagePart | null {
  const match = /^data:([^;]+);base64,(.+)$/i.exec(url.trim());
  if (!match) return null;
  try {
    const data = Uint8Array.from(atob(match[2]!), (char) => char.charCodeAt(0));
    return { mimeType: match[1] || "image/png", data };
  } catch {
    return null;
  }
}

async function loadImagePart(part: ContentPart): Promise<ParsedImagePart | null> {
  if (part.type !== "image_url" && part.type !== "input_image") return null;
  const url = imageURLFromPart(part);
  if (!url) return null;
  if (url.startsWith("data:")) {
    return parseDataURL(url);
  }
  try {
    const response = await fetch(url);
    if (!response.ok) return null;
    const mimeType = response.headers.get("content-type")?.split(";")[0]?.trim() || "image/png";
    return { mimeType, data: new Uint8Array(await response.arrayBuffer()) };
  } catch {
    return null;
  }
}

async function extractImagesFromLastUserMessage(messages: OpenAIMessage[]): Promise<ParsedImagePart[]> {
  for (let index = messages.length - 1; index >= 0; index -= 1) {
    const message = messages[index];
    if (message?.role !== "user" || message.content == null || typeof message.content === "string") {
      continue;
    }
    const images: ParsedImagePart[] = [];
    for (const part of message.content) {
      const loaded = await loadImagePart(part);
      if (loaded) images.push(loaded);
    }
    return images;
  }
  return [];
}

function buildSelectedImage(image: ParsedImagePart, blobStore: Map<string, Uint8Array>): SelectedImage {
  // Index under SHA-256 and identity keys so later GetBlob variants resolve.
  const blobId = indexBlob(blobStore, image.data);
  return create(SelectedImageSchema, {
    uuid: crypto.randomUUID(),
    path: `upload://${crypto.randomUUID()}.png`,
    mimeType: image.mimeType || "image/png",
    dimension: create(SelectedImage_DimensionSchema, { width: 0, height: 0 }),
    dataOrBlobId: {
      case: "blobIdWithData",
      value: create(SelectedImage_BlobIdWithDataSchema, {
        blobId,
        data: image.data,
      }),
    },
  });
}

function parseMessages(messages: OpenAIMessage[]): ParsedMessages {
  let systemPrompt = "You are a helpful assistant.";
  const pairs: Array<{ userText: string; assistantText: string }> = [];
  const toolResults: ToolResultInfo[] = [];

  // Collect system messages
  const systemParts = messages
    .filter((m) => m.role === "system")
    .map((m) => textContent(m.content));
  if (systemParts.length > 0) {
    systemPrompt = systemParts.join("\n");
  }

  // Separate tool results from conversation turns
  const nonSystem = messages.filter((m) => m.role !== "system");
  let pendingUser = "";

  for (const msg of nonSystem) {
    if (msg.role === "tool") {
      toolResults.push({
        toolCallId: msg.tool_call_id ?? "",
        content: textContent(msg.content),
      });
    } else if (msg.role === "user") {
      if (pendingUser) {
        pairs.push({ userText: pendingUser, assistantText: "" });
      }
      pendingUser = textContent(msg.content);
    } else if (msg.role === "assistant") {
      // Skip assistant messages that are just tool_calls with no text
      const text = textContent(msg.content);
      if (pendingUser) {
        pairs.push({ userText: pendingUser, assistantText: text });
        pendingUser = "";
      }
    }
  }

  let lastUserText = "";
  if (pendingUser) {
    lastUserText = pendingUser;
  } else if (pairs.length > 0 && toolResults.length === 0) {
    const last = pairs.pop()!;
    lastUserText = last.userText;
  }

  return { systemPrompt, userText: lastUserText, turns: pairs, toolResults };
}

function normalizeOpenAIToolDef(tool: unknown): NormalizedFunctionTool | null {
  if (!tool || typeof tool !== "object") return null;
  const entry = tool as OpenAIToolDef;
  if (entry.type !== "function") return null;

  if (entry.function && typeof entry.function === "object") {
    const name = entry.function.name?.trim();
    if (!name) return null;
    return {
      name,
      description: entry.function.description,
      parameters: entry.function.parameters,
    };
  }

  const name = entry.name?.trim();
  if (!name) return null;
  return {
    name,
    description: entry.description,
    parameters: entry.parameters,
  };
}

function collectChatTools(rawTools: unknown[] | OpenAIToolDef[] | undefined): NormalizedFunctionTool[] {
  const normalized: NormalizedFunctionTool[] = [];
  for (const tool of rawTools ?? []) {
    const item = normalizeOpenAIToolDef(tool);
    if (item) normalized.push(item);
  }
  return normalized;
}

/** Convert OpenAI tool definitions to Cursor's MCP tool protobuf format. */
function buildMcpToolDefinitions(tools: NormalizedFunctionTool[]): McpToolDefinition[] {
  const out: McpToolDefinition[] = [];
  for (const fn of tools) {
    try {
      const jsonSchema: JsonValue =
        fn.parameters && typeof fn.parameters === "object"
          ? (fn.parameters as JsonValue)
          : { type: "object", properties: {}, required: [] };
      const inputSchema = toBinary(ValueSchema, fromJson(ValueSchema, jsonSchema));
      out.push(create(McpToolDefinitionSchema, {
        name: fn.name,
        description: fn.description || "",
        providerIdentifier: "opencode",
        toolName: fn.name,
        inputSchema,
      }));
    } catch (err) {
      console.warn(`[cursor-bridge] skip tool ${fn.name}:`, err);
    }
  }
  return out;
}

/** Decode a Cursor MCP arg value (protobuf Value bytes) to a JS value. */
function decodeMcpArgValue(value: Uint8Array): unknown {
  try {
    const parsed = fromBinary(ValueSchema, value);
    return toJson(ValueSchema, parsed);
  } catch {}
  return new TextDecoder().decode(value);
}

/** Decode a map of MCP arg values. */
function decodeMcpArgsMap(args: Record<string, Uint8Array>): Record<string, unknown> {
  const decoded: Record<string, unknown> = {};
  for (const [key, value] of Object.entries(args)) {
    decoded[key] = decodeMcpArgValue(value);
  }
  return decoded;
}

function buildCursorRequest(
  modelId: string,
  systemPrompt: string,
  userText: string,
  turns: Array<{ userText: string; assistantText: string }>,
  conversationId: string,
  checkpoint: Uint8Array | null,
  existingBlobStore?: Map<string, Uint8Array>,
  selectedImages: SelectedImage[] = [],
): CursorRequestPayload {
  const blobStore = new Map<string, Uint8Array>(existingBlobStore ?? []);

  // System prompt → blob store (Cursor requests it back via KV handshake)
  const systemJson = JSON.stringify({ role: "system", content: systemPrompt });
  const systemBytes = new TextEncoder().encode(systemJson);
  const systemBlobId = indexBlob(blobStore, systemBytes);

  let conversationState;
  if (checkpoint) {
    conversationState = fromBinary(ConversationStateStructureSchema, checkpoint);
  } else {
    const turnBytes: Uint8Array[] = [];
    for (const turn of turns) {
      const userMsg = create(UserMessageSchema, {
        text: turn.userText,
        messageId: crypto.randomUUID(),
      });
      const userMsgBytes = toBinary(UserMessageSchema, userMsg);
      // Pre-register nested message/turn blobs — Cursor may GetBlob by raw bytes.
      indexBlob(blobStore, userMsgBytes);

      const stepBytes: Uint8Array[] = [];
      if (turn.assistantText) {
        const step = create(ConversationStepSchema, {
          message: {
            case: "assistantMessage",
            value: create(AssistantMessageSchema, { text: turn.assistantText }),
          },
        });
        stepBytes.push(toBinary(ConversationStepSchema, step));
      }

      const agentTurn = create(AgentConversationTurnStructureSchema, {
        userMessage: userMsgBytes,
        steps: stepBytes,
      });
      const agentTurnBytes = toBinary(AgentConversationTurnStructureSchema, agentTurn);
      indexBlob(blobStore, agentTurnBytes);
      const turnStructure = create(ConversationTurnStructureSchema, {
        turn: { case: "agentConversationTurn", value: agentTurn },
      });
      const turnStructureBytes = toBinary(ConversationTurnStructureSchema, turnStructure);
      indexBlob(blobStore, turnStructureBytes);
      turnBytes.push(turnStructureBytes);
    }

    conversationState = create(ConversationStateStructureSchema, {
      rootPromptMessagesJson: [systemBlobId],
      turns: turnBytes,
      todos: [],
      pendingToolCalls: [],
      previousWorkspaceUris: [],
      fileStates: {},
      fileStatesV2: {},
      summaryArchives: [],
      turnTimings: [],
      subagentStates: {},
      selfSummaryCount: 0,
      readPaths: [],
    });
  }

  const userMessage = create(UserMessageSchema, {
    text: userText,
    messageId: crypto.randomUUID(),
    ...(selectedImages.length > 0
      ? {
          selectedContext: create(SelectedContextSchema, {
            selectedImages,
            extraContext: [],
            extraContextEntries: [],
            files: [],
            codeSelections: [],
            terminals: [],
            terminalSelections: [],
            folders: [],
            externalLinks: [],
            cursorRules: [],
            cursorCommands: [],
          }),
        }
      : {}),
  });
  // Pre-register the outgoing user message (often embeds <environment_context>).
  const outgoingUserMsgBytes = toBinary(UserMessageSchema, userMessage);
  indexBlob(blobStore, outgoingUserMsgBytes);
  if (userText) {
    const userJson = JSON.stringify({ role: "user", content: userText });
    indexBlob(blobStore, new TextEncoder().encode(userJson));
  }
  const action = create(ConversationActionSchema, {
    action: {
      case: "userMessageAction",
      value: create(UserMessageActionSchema, { userMessage }),
    },
  });

  const modelDetails = create(ModelDetailsSchema, {
    modelId,
    displayModelId: modelId,
    displayName: modelId,
  });

  const runRequest = create(AgentRunRequestSchema, {
    conversationState,
    action,
    modelDetails,
    conversationId,
  });

  const clientMessage = create(AgentClientMessageSchema, {
    message: { case: "runRequest", value: runRequest },
  });

  return {
    requestBytes: toBinary(AgentClientMessageSchema, clientMessage),
    blobStore,
    mcpTools: [],
  };
}

function parseConnectEndStream(data: Uint8Array): { code: string; message: string } | null {
  try {
    const payload = JSON.parse(new TextDecoder().decode(data));
    const error = payload?.error;
    if (error) {
      const code = String(error.code ?? "unknown");
      const message = String(error.message ?? "Unknown error");
      return { code, message: `Connect error ${code}: ${message}` };
    }
    return null;
  } catch {
    return { code: "unknown", message: "Failed to parse Connect end stream" };
  }
}

function makeHeartbeatBytes(): Uint8Array {
  const heartbeat = create(AgentClientMessageSchema, {
    message: {
      case: "clientHeartbeat",
      value: create(ClientHeartbeatSchema, {}),
    },
  });
  return frameConnectMessage(toBinary(AgentClientMessageSchema, heartbeat));
}

/**
 * Create a stateful parser for Connect protocol frames.
 * Handles buffering partial data across chunks.
 */
function createConnectFrameParser(
  onMessage: (bytes: Uint8Array) => void,
  onEndStream: (bytes: Uint8Array) => void,
): (incoming: Buffer) => void {
  let pending = Buffer.alloc(0);
  return (incoming: Buffer) => {
    pending = Buffer.concat([pending, incoming]);
    while (pending.length >= 5) {
      const flags = pending[0]!;
      const msgLen = pending.readUInt32BE(1);
      if (pending.length < 5 + msgLen) break;
      const messageBytes = pending.subarray(5, 5 + msgLen);
      pending = pending.subarray(5 + msgLen);
      if (flags & CONNECT_END_STREAM_FLAG) {
        onEndStream(messageBytes);
      } else {
        onMessage(messageBytes);
      }
    }
  };
}

const THINKING_TAG_NAMES = ['think', 'thinking', 'reasoning', 'thought', 'think_intent'];
const MAX_THINKING_TAG_LEN = 16; // </think_intent> is 15 chars

/**
 * Strip thinking tags from streamed text, routing tagged content to reasoning.
 * Buffers partial tags across chunk boundaries.
 */
function createThinkingTagFilter(): {
  process(text: string): { content: string; reasoning: string };
  flush(): { content: string; reasoning: string };
} {
  let buffer = '';
  let inThinking = false;

  return {
    process(text: string) {
      const input = buffer + text;
      buffer = '';
      let content = '';
      let reasoning = '';
      let lastIdx = 0;

      const re = new RegExp(`<(/?)(?:${THINKING_TAG_NAMES.join('|')})\\s*>`, 'gi');
      let match: RegExpExecArray | null;
      while ((match = re.exec(input)) !== null) {
        const before = input.slice(lastIdx, match.index);
        if (inThinking) reasoning += before;
        else content += before;
        inThinking = match[1] !== '/';
        lastIdx = re.lastIndex;
      }

      const rest = input.slice(lastIdx);
      // Buffer a trailing '<' that could be the start of a thinking tag.
      const ltPos = rest.lastIndexOf('<');
      if (ltPos >= 0 && rest.length - ltPos < MAX_THINKING_TAG_LEN && /^<\/?[a-z_]*$/i.test(rest.slice(ltPos))) {
        buffer = rest.slice(ltPos);
        const before = rest.slice(0, ltPos);
        if (inThinking) reasoning += before;
        else content += before;
      } else {
        if (inThinking) reasoning += rest;
        else content += rest;
      }

      return { content, reasoning };
    },
    flush() {
      const b = buffer;
      buffer = '';
      if (!b) return { content: '', reasoning: '' };
      return inThinking ? { content: '', reasoning: b } : { content: b, reasoning: '' };
    },
  };
}

interface StreamState {
  toolCallIndex: number;
  pendingExecs: PendingExec[];
}

function processServerMessage(
  msg: AgentServerMessage,
  blobStore: Map<string, Uint8Array>,
  mcpTools: McpToolDefinition[],
  sendFrame: (data: Uint8Array) => void,
  state: StreamState,
  onText: (text: string, isThinking?: boolean) => void,
  onMcpExec: (exec: PendingExec) => void,
  onCheckpoint?: (checkpointBytes: Uint8Array) => void,
  onImage?: (imageData: string) => void,
): void {
  const msgCase = msg.message.case;

  if (msgCase === "interactionUpdate") {
    handleInteractionUpdate(msg.message.value, onText, onImage);
  } else if (msgCase === "kvServerMessage") {
    handleKvMessage(msg.message.value as KvServerMessage, blobStore, sendFrame);
  } else if (msgCase === "execServerMessage") {
    handleExecMessage(
      msg.message.value as ExecServerMessage,
      mcpTools,
      sendFrame,
      onMcpExec,
    );
  } else if (msgCase === "conversationCheckpointUpdate" && onCheckpoint) {
    onCheckpoint(
      toBinary(ConversationStateStructureSchema, msg.message.value as ConversationStateStructure),
    );
  }
}

function extractGenerateImageData(toolCall: any, onImage: (imageData: string) => void): void {
  if (toolCall?.tool?.case !== "generateImageToolCall") return;
  const result = toolCall.tool.value.result;
  if (result?.result?.case === "success" && result.result.value.imageData) {
    onImage(result.result.value.imageData);
  }
}

function handleInteractionUpdate(
  update: any,
  onText: (text: string, isThinking?: boolean) => void,
  onImage?: (imageData: string) => void,
): void {
  const updateCase = update.message?.case;

  if (updateCase === "textDelta") {
    const delta = update.message.value.text || "";
    if (delta) onText(delta, false);
  } else if (updateCase === "thinkingDelta") {
    const delta = update.message.value.text || "";
    if (delta) onText(delta, true);
  } else if (updateCase === "toolCallStarted" && onImage) {
    extractGenerateImageData(update.message.value.toolCall, onImage);
  } else if (updateCase === "toolCallCompleted" && onImage) {
    extractGenerateImageData(update.message.value.toolCall, onImage);
  }
  // toolCallStarted, partialToolCall, toolCallDelta, toolCallCompleted
  // are intentionally ignored. MCP tool calls flow through the exec
  // message path (mcpArgs → mcpResult), not interaction updates.
}

/** Send a KV client response back to Cursor. */
function sendKvResponse(
  kvMsg: KvServerMessage,
  messageCase: string,
  value: unknown,
  sendFrame: (data: Uint8Array) => void,
): void {
  const response = create(KvClientMessageSchema, {
    id: kvMsg.id,
    message: { case: messageCase as any, value: value as any },
  });
  const clientMsg = create(AgentClientMessageSchema, {
    message: { case: "kvClientMessage", value: response },
  });
  sendFrame(frameConnectMessage(toBinary(AgentClientMessageSchema, clientMsg)));
}

/**
 * Cursor sometimes calls GetBlob with a content-shaped id (serialized
 * UserMessage / ConversationTurn protobuf that embeds <environment_context>),
 * not a 32-byte SHA-256. When the hash map misses, treat those bytes as the
 * blob payload so Connect does not fail with "Blob not found".
 */
function isContentLikeBlobId(blobId: Uint8Array): boolean {
  if (blobId.length <= 32) return false;
  try {
    const text = new TextDecoder().decode(blobId);
    if (
      text.includes("environment_context") ||
      text.includes("<cwd>") ||
      text.includes('"role"') ||
      text.includes("user_message")
    ) {
      return true;
    }
  } catch {
    // fall through to printable-ratio check
  }
  let printable = 0;
  for (const b of blobId) {
    if ((b >= 32 && b < 127) || b === 9 || b === 10 || b === 13) printable++;
  }
  return blobId.length > 64 && printable / blobId.length > 0.45;
}

function resolveBlobData(
  blobId: Uint8Array,
  blobStore: Map<string, Uint8Array>,
): Uint8Array | undefined {
  const blobIdKey = Buffer.from(blobId).toString("hex");
  const stored = blobStore.get(blobIdKey);
  if (stored) return stored;
  if (isContentLikeBlobId(blobId)) {
    // Identity mapping: id bytes are the payload Cursor expects.
    blobStore.set(blobIdKey, blobId);
    return blobId;
  }
  return undefined;
}

/** Index blob under both its SHA-256 id and raw bytes (identity) keys. */
function indexBlob(blobStore: Map<string, Uint8Array>, data: Uint8Array): Uint8Array {
  const hashId = new Uint8Array(createHash("sha256").update(data).digest());
  blobStore.set(Buffer.from(hashId).toString("hex"), data);
  blobStore.set(Buffer.from(data).toString("hex"), data);
  return hashId;
}

function handleKvMessage(
  kvMsg: KvServerMessage,
  blobStore: Map<string, Uint8Array>,
  sendFrame: (data: Uint8Array) => void,
): void {
  const kvCase = kvMsg.message.case;

  if (kvCase === "getBlobArgs") {
    const blobId = kvMsg.message.value.blobId;
    const blobData = resolveBlobData(blobId, blobStore);
    if (!blobData) {
      console.warn(
        `[cursor-bridge] GetBlob miss idLen=${blobId.length} hex=${Buffer.from(blobId).toString("hex").slice(0, 24)}…`,
      );
      try {
        appendFileSync(
          "/tmp/cursor-bridge-debug.log",
          `[${new Date().toISOString()}] GetBlob MISS idLen=${blobId.length} hex=${Buffer.from(blobId).toString("hex").slice(0, 48)} storeSize=${blobStore.size}\n`,
        );
      } catch {}
    }
    sendKvResponse(
      kvMsg, "getBlobResult",
      create(GetBlobResultSchema, blobData ? { blobData } : {}),
      sendFrame,
    );
  } else if (kvCase === "setBlobArgs") {
    const { blobId, blobData } = kvMsg.message.value;
    blobStore.set(Buffer.from(blobId).toString("hex"), blobData);
    // Also index by content hash / identity for later GetBlob variants.
    indexBlob(blobStore, blobData);
    sendKvResponse(
      kvMsg, "setBlobResult",
      create(SetBlobResultSchema, {}),
      sendFrame,
    );
  }
}

function handleExecMessage(
  execMsg: ExecServerMessage,
  mcpTools: McpToolDefinition[],
  sendFrame: (data: Uint8Array) => void,
  onMcpExec: (exec: PendingExec) => void,
): void {
  const execCase = execMsg.message.case;
  if (execCase && execCase !== "requestContextArgs") {
    console.error(`[cursor-bridge] exec case=${execCase}`);
  }

  if (execCase === "requestContextArgs") {
    const requestContext = create(RequestContextSchema, {
      rules: [],
      repositoryInfo: [],
      tools: mcpTools,
      gitRepos: [],
      projectLayouts: [],
      mcpInstructions: [],
      fileContents: {},
      customSubagents: [],
    });
    const result = create(RequestContextResultSchema, {
      result: {
        case: "success",
        value: create(RequestContextSuccessSchema, { requestContext }),
      },
    });
    sendExecResult(execMsg, "requestContextResult", result, sendFrame);
    return;
  }

  if (execCase === "mcpArgs") {
    const mcpArgs = execMsg.message.value;
    const decoded = decodeMcpArgsMap(mcpArgs.args ?? {});
    const rawToolCallId = String(mcpArgs.toolCallId ?? "");
    const toolCallId = sanitizeToolCallId(rawToolCallId);
    const toolName = String(mcpArgs.toolName || mcpArgs.name || "");
    if (rawToolCallId && rawToolCallId !== toolCallId) {
      console.warn(
        `[cursor-bridge] sanitized toolCallId ${JSON.stringify(rawToolCallId)} -> ${toolCallId}`,
      );
    }
    // Codex's view_image is a local client tool, but Cursor may invoke it via
    // MCP after we register Codex tools. The image is already in selectedContext;
    // completing in-process avoids a second HTTP round-trip that often hangs.
    if (toolName === "view_image" || toolName === "ViewImage") {
      try {
        appendFileSync(
          "/tmp/cursor-bridge-debug.log",
          `[${new Date().toISOString()}] in-process mcp ${toolName} id=${toolCallId} args=${JSON.stringify(decoded).slice(0, 160)}\n`,
        );
      } catch {}
      const mcpResult = create(McpResultSchema, {
        result: {
          case: "success",
          value: create(McpSuccessSchema, {
            content: [
              create(McpToolResultContentItemSchema, {
                content: {
                  case: "text",
                  value: create(McpTextContentSchema, {
                    text: "Image is already available in the current message context. Describe it directly.",
                  }),
                },
              }),
            ],
            isError: false,
          }),
        },
      });
      sendExecResult(execMsg, "mcpResult", mcpResult, sendFrame);
      return;
    }
    onMcpExec({
      execId: execMsg.execId,
      execMsgId: execMsg.id,
      toolCallId,
      toolName,
      decodedArgs: JSON.stringify(decoded),
    });
    return;
  }

  // --- Reject native Cursor tools ---
  // The model tries these first. We must respond with rejection/error
  // so it falls back to our MCP tools (registered via RequestContext).
  const REJECT_REASON = "Tool not available in this environment. Use the MCP tools provided instead.";

  if (execCase === "readArgs") {
    const args = execMsg.message.value;
    const result = create(ReadResultSchema, {
      result: { case: "rejected", value: create(ReadRejectedSchema, { path: args.path, reason: REJECT_REASON }) },
    });
    sendExecResult(execMsg, "readResult", result, sendFrame);
    return;
  }
  if (execCase === "lsArgs") {
    const args = execMsg.message.value;
    const result = create(LsResultSchema, {
      result: { case: "rejected", value: create(LsRejectedSchema, { path: args.path, reason: REJECT_REASON }) },
    });
    sendExecResult(execMsg, "lsResult", result, sendFrame);
    return;
  }
  if (execCase === "grepArgs") {
    const result = create(GrepResultSchema, {
      result: { case: "error", value: create(GrepErrorSchema, { error: REJECT_REASON }) },
    });
    sendExecResult(execMsg, "grepResult", result, sendFrame);
    return;
  }
  if (execCase === "writeArgs") {
    const args = execMsg.message.value;
    const result = create(WriteResultSchema, {
      result: { case: "rejected", value: create(WriteRejectedSchema, { path: args.path, reason: REJECT_REASON }) },
    });
    sendExecResult(execMsg, "writeResult", result, sendFrame);
    return;
  }
  if (execCase === "deleteArgs") {
    const args = execMsg.message.value;
    const result = create(DeleteResultSchema, {
      result: { case: "rejected", value: create(DeleteRejectedSchema, { path: args.path, reason: REJECT_REASON }) },
    });
    sendExecResult(execMsg, "deleteResult", result, sendFrame);
    return;
  }
  if (execCase === "shellArgs" || execCase === "shellStreamArgs") {
    const args = execMsg.message.value as {
      command?: string;
      workingDirectory?: string;
      toolCallId?: string;
    };
    const command = args.command ?? "";
    const cwd = args.workingDirectory || process.cwd();
    const clientTool = findClientShellToolName(mcpTools);
    // When the OpenAI client advertised a shell tool (Codex exec_command etc.),
    // run the command locally and complete Cursor's native shell request
    // in-process. shellStreamArgs requires start→stdout/stderr→exit→shellResult
    // →execClientControlMessage.streamClose (matches Cursor/oh-my-pi protocol);
    // omitting streamClose leaves the agent turn pending forever.
    if (clientTool && command) {
      try {
        appendFileSync(
          "/tmp/cursor-bridge-debug.log",
          `[${new Date().toISOString()}] local-shell ${execCase} cmd=${JSON.stringify(command).slice(0, 160)}\n`,
        );
      } catch {}
      // Async so we don't block the H2/SSE event loop while the command runs.
      (async () => {
        try {
          const proc = Bun.spawn(["/bin/zsh", "-lc", command], {
            cwd,
            stdout: "pipe",
            stderr: "pipe",
            env: process.env,
          });
          const [stdout, stderr, code] = await Promise.all([
            new Response(proc.stdout).text(),
            new Response(proc.stderr).text(),
            proc.exited,
          ]);
          const exitCode = code < 0 ? 1 : code;
          const shellSuccess = create(ShellResultSchema, {
            result: {
              case: "success",
              value: create(ShellSuccessSchema, {
                command,
                workingDirectory: cwd,
                exitCode,
                signal: "",
                stdout,
                stderr,
                executionTime: 0,
                interleavedOutput: `${stdout}${stderr}`,
              }),
            },
          });
          if (execCase === "shellStreamArgs") {
            sendExecResult(
              execMsg,
              "shellStream",
              create(ShellStreamSchema, {
                event: { case: "start", value: create(ShellStreamStartSchema, {}) },
              }),
              sendFrame,
            );
            if (stdout) {
              sendExecResult(
                execMsg,
                "shellStream",
                create(ShellStreamSchema, {
                  event: {
                    case: "stdout",
                    value: create(ShellStreamStdoutSchema, { data: stdout }),
                  },
                }),
                sendFrame,
              );
            }
            if (stderr) {
              sendExecResult(
                execMsg,
                "shellStream",
                create(ShellStreamSchema, {
                  event: {
                    case: "stderr",
                    value: create(ShellStreamStderrSchema, { data: stderr }),
                  },
                }),
                sendFrame,
              );
            }
            sendExecResult(
              execMsg,
              "shellStream",
              create(ShellStreamSchema, {
                event: {
                  case: "exit",
                  value: create(ShellStreamExitSchema, {
                    code: exitCode,
                    cwd,
                    aborted: false,
                  }),
                },
              }),
              sendFrame,
            );
            // Cursor can keep the turn pending on stream deltas alone.
            sendExecResult(execMsg, "shellResult", shellSuccess, sendFrame);
            sendExecClientStreamClose(execMsg, sendFrame);
          } else {
            sendExecResult(execMsg, "shellResult", shellSuccess, sendFrame);
          }
          try {
            appendFileSync(
              "/tmp/cursor-bridge-debug.log",
              `[${new Date().toISOString()}] local-shell done streamClose=${execCase === "shellStreamArgs"} code=${exitCode} out=${JSON.stringify(stdout).slice(0, 120)}\n`,
            );
          } catch {}
        } catch (err) {
          const message = err instanceof Error ? err.message : String(err);
          if (execCase === "shellStreamArgs") {
            sendExecResult(
              execMsg,
              "shellStream",
              create(ShellStreamSchema, {
                event: { case: "start", value: create(ShellStreamStartSchema, {}) },
              }),
              sendFrame,
            );
            sendExecResult(
              execMsg,
              "shellStream",
              create(ShellStreamSchema, {
                event: {
                  case: "stderr",
                  value: create(ShellStreamStderrSchema, { data: message }),
                },
              }),
              sendFrame,
            );
            sendExecResult(
              execMsg,
              "shellStream",
              create(ShellStreamSchema, {
                event: {
                  case: "exit",
                  value: create(ShellStreamExitSchema, {
                    code: 1,
                    cwd,
                    aborted: false,
                  }),
                },
              }),
              sendFrame,
            );
          }
          sendExecResult(
            execMsg,
            "shellResult",
            create(ShellResultSchema, {
              result: {
                case: "rejected",
                value: create(ShellRejectedSchema, {
                  command,
                  workingDirectory: cwd,
                  reason: message,
                  isReadonly: false,
                }),
              },
            }),
            sendFrame,
          );
          if (execCase === "shellStreamArgs") {
            sendExecClientStreamClose(execMsg, sendFrame);
          }
        }
      })();
      return;
    }
    const rejected = create(ShellResultSchema, {
      result: {
        case: "rejected",
        value: create(ShellRejectedSchema, {
          command,
          workingDirectory: cwd,
          reason: REJECT_REASON,
          isReadonly: false,
        }),
      },
    });
    if (execCase === "shellStreamArgs") {
      sendExecResult(
        execMsg,
        "shellStream",
        create(ShellStreamSchema, {
          event: { case: "start", value: create(ShellStreamStartSchema, {}) },
        }),
        sendFrame,
      );
      sendExecResult(
        execMsg,
        "shellStream",
        create(ShellStreamSchema, {
          event: {
            case: "exit",
            value: create(ShellStreamExitSchema, {
              code: 1,
              cwd,
              aborted: false,
            }),
          },
        }),
        sendFrame,
      );
      sendExecResult(execMsg, "shellResult", rejected, sendFrame);
      sendExecClientStreamClose(execMsg, sendFrame);
    } else {
      sendExecResult(execMsg, "shellResult", rejected, sendFrame);
    }
    return;
  }
  if (execCase === "backgroundShellSpawnArgs") {
    const args = execMsg.message.value;
    const result = create(BackgroundShellSpawnResultSchema, {
      result: {
        case: "rejected",
        value: create(ShellRejectedSchema, {
          command: args.command ?? "",
          workingDirectory: args.workingDirectory ?? "",
          reason: REJECT_REASON,
          isReadonly: false,
        }),
      },
    });
    sendExecResult(execMsg, "backgroundShellSpawnResult", result, sendFrame);
    return;
  }
  if (execCase === "writeShellStdinArgs") {
    const result = create(WriteShellStdinResultSchema, {
      result: { case: "error", value: create(WriteShellStdinErrorSchema, { error: REJECT_REASON }) },
    });
    sendExecResult(execMsg, "writeShellStdinResult", result, sendFrame);
    return;
  }
  if (execCase === "fetchArgs") {
    const args = execMsg.message.value;
    const result = create(FetchResultSchema, {
      result: { case: "error", value: create(FetchErrorSchema, { url: args.url ?? "", error: REJECT_REASON }) },
    });
    sendExecResult(execMsg, "fetchResult", result, sendFrame);
    return;
  }
  if (execCase === "diagnosticsArgs") {
    const result = create(DiagnosticsResultSchema, {});
    sendExecResult(execMsg, "diagnosticsResult", result, sendFrame);
    return;
  }

  // MCP resource/screen/computer exec types
  const miscCaseMap: Record<string, string> = {
    listMcpResourcesExecArgs: "listMcpResourcesExecResult",
    readMcpResourceExecArgs: "readMcpResourceExecResult",
    recordScreenArgs: "recordScreenResult",
    computerUseArgs: "computerUseResult",
  };
  const resultCase = miscCaseMap[execCase as string];
  if (resultCase) {
    sendExecResult(execMsg, resultCase, create(McpResultSchema, {}), sendFrame);
    return;
  }

  // Unknown exec type — log and ignore
  console.error(`[proxy] unhandled exec: ${execCase}`);
}

/** Send an exec client message back to Cursor. */
function sendExecResult(
  execMsg: ExecServerMessage,
  messageCase: string,
  value: unknown,
  sendFrame: (data: Uint8Array) => void,
): void {
  const execClientMessage = create(ExecClientMessageSchema, {
    id: execMsg.id,
    execId: execMsg.execId,
    message: { case: messageCase as any, value: value as any },
  });
  const clientMessage = create(AgentClientMessageSchema, {
    message: { case: "execClientMessage", value: execClientMessage },
  });
  sendFrame(frameConnectMessage(toBinary(AgentClientMessageSchema, clientMessage)));
}

/** Close a shellStreamArgs exec; without this Cursor leaves the turn pending. */
function sendExecClientStreamClose(
  execMsg: ExecServerMessage,
  sendFrame: (data: Uint8Array) => void,
): void {
  const closeMessage = create(ExecClientControlMessageSchema, {
    message: {
      case: "streamClose",
      value: create(ExecClientStreamCloseSchema, { id: execMsg.id }),
    },
  });
  const clientMessage = create(AgentClientMessageSchema, {
    message: { case: "execClientControlMessage", value: closeMessage },
  });
  sendFrame(frameConnectMessage(toBinary(AgentClientMessageSchema, clientMessage)));
}

/** Derive a stable key to associate a bridge with a conversation. */
function deriveBridgeKey(modelId: string, messages: OpenAIMessage[]): string {
  // Codex always prefixes a shared <permissions>/<skills> user message. Using
  // the first user text would collide every session onto one bridge/blobStore
  // (Blob not found + cross-talk). Prefer the latest non-harness user text.
  let best = "";
  for (const msg of messages) {
    if (msg.role !== "user") continue;
    const text = textContent(msg.content).trim();
    if (!text) continue;
    if (
      text.includes("<permissions instructions>") ||
      text.includes("<skills_instructions>") ||
      text.includes("<app-context>")
    ) {
      continue;
    }
    best = text;
  }
  if (!best) {
    const first = messages.find((msg) => msg.role === "user");
    best = first ? textContent(first.content) : "";
  }
  return createHash("sha256")
    .update(`${modelId}:${best.slice(0, 400)}`)
    .digest("hex")
    .slice(0, 16);
}

/** Create an SSE streaming Response that reads from a live bridge. */
function createBridgeStreamResponse(
  bridge: ReturnType<typeof spawnBridge>,
  heartbeatTimer: NodeJS.Timeout,
  blobStore: Map<string, Uint8Array>,
  mcpTools: McpToolDefinition[],
  modelId: string,
  bridgeKey: string,
  onReady?: () => void,
): Response {
  const completionId = `chatcmpl-${crypto.randomUUID().replace(/-/g, "").slice(0, 28)}`;
  const created = Math.floor(Date.now() / 1000);

  const stream = new ReadableStream({
    start(controller) {
      const encoder = new TextEncoder();
      let closed = false;
      let streamError: { code: string; message: string } | null = null;
      const sendSSE = (data: object) => {
        if (closed) return;
        controller.enqueue(encoder.encode(`data: ${JSON.stringify(data)}\n\n`));
      };
      const sendDone = () => {
        if (closed) return;
        controller.enqueue(encoder.encode("data: [DONE]\n\n"));
      };
      const closeController = () => {
        if (closed) return;
        closed = true;
        controller.close();
      };
      const failStream = (err: { code?: string; message: string }) => {
        if (closed) return;
        sendSSE(openAIErrorPayload(err.message, err.code));
        sendDone();
        closeController();
      };

      const makeChunk = (
        delta: Record<string, unknown>,
        finishReason: string | null = null,
      ) => ({
        id: completionId,
        object: "chat.completion.chunk",
        created,
        model: modelId,
        choices: [{ index: 0, delta, finish_reason: finishReason }],
      });

      const state: StreamState = {
        toolCallIndex: 0,
        pendingExecs: [],
      };
      const tagFilter = createThinkingTagFilter();

      let mcpExecReceived = false;

      const processChunk = createConnectFrameParser(
        (messageBytes) => {
          try {
            const serverMessage = fromBinary(
              AgentServerMessageSchema,
              messageBytes,
            );
            processServerMessage(
              serverMessage,
              blobStore,
              mcpTools,
              (data) => bridge.write(data),
              state,
              (text, isThinking) => {
                if (isThinking) {
                  sendSSE(makeChunk({ reasoning_content: text }));
                } else {
                  const { content, reasoning } = tagFilter.process(text);
                  if (reasoning) sendSSE(makeChunk({ reasoning_content: reasoning }));
                  if (content) sendSSE(makeChunk({ content }));
                }
              },
              // onMcpExec — the model wants to execute a tool.
              (exec) => {
                state.pendingExecs.push(exec);
                mcpExecReceived = true;

                const flushed = tagFilter.flush();
                if (flushed.reasoning) sendSSE(makeChunk({ reasoning_content: flushed.reasoning }));
                if (flushed.content) sendSSE(makeChunk({ content: flushed.content }));

                const toolCallIndex = state.toolCallIndex++;
                sendSSE(makeChunk({
                  tool_calls: [{
                    index: toolCallIndex,
                    id: exec.toolCallId,
                    type: "function",
                    function: {
                      name: exec.toolName,
                      arguments: exec.decodedArgs,
                    },
                  }],
                }));

                // Keep the bridge alive for tool result continuation.
                activeBridges.set(bridgeKey, {
                  bridge,
                  heartbeatTimer,
                  blobStore,
                  mcpTools,
                  pendingExecs: state.pendingExecs,
                });

                sendSSE(makeChunk({}, "tool_calls"));
                sendDone();
                closeController();
              },
              (checkpointBytes) => {
                const stored = conversationStates.get(bridgeKey);
                if (stored) {
                  stored.checkpoint = checkpointBytes;
                  stored.lastAccessMs = Date.now();
                }
              },
            );
          } catch {
            // Skip unparseable messages
          }
        },
        (endStreamBytes) => {
          const endError = parseConnectEndStream(endStreamBytes);
          if (endError) {
            streamError = endError;
          }
        },
      );

      bridge.onData(processChunk);

      bridge.onClose((code) => {
        clearInterval(heartbeatTimer);
        const stored = conversationStates.get(bridgeKey);
        if (stored) {
          for (const [k, v] of blobStore) stored.blobStore.set(k, v);
          stored.lastAccessMs = Date.now();
        }
        if (!mcpExecReceived) {
          if (streamError) {
            failStream(streamError);
            return;
          }
          const flushed = tagFilter.flush();
          if (flushed.reasoning) sendSSE(makeChunk({ reasoning_content: flushed.reasoning }));
          if (flushed.content) sendSSE(makeChunk({ content: flushed.content }));
          sendSSE(makeChunk({}, "stop"));
          sendDone();
          closeController();
        } else if (code !== 0) {
          // Bridge died while tool calls are pending (timeout, crash, etc.).
          // Close the SSE stream so the client doesn't hang forever.
          failStream({ code: "bridge_lost", message: "bridge connection lost" });
          // Remove stale entry so the next request doesn't try to resume it.
          activeBridges.delete(bridgeKey);
        }
      });

      // Resume tool results only after listeners are attached, otherwise a
      // fast Cursor continuation can finish before we observe any bytes.
      try {
        onReady?.();
      } catch (err) {
        const message = err instanceof Error ? err.message : String(err);
        failStream({ code: "internal_error", message });
      }
    },
  });

  return new Response(stream, { headers: SSE_HEADERS });
}

/** Spawn a bridge, send the initial request frame, and start heartbeat. */
function startBridge(
  accessToken: string,
  requestBytes: Uint8Array,
): { bridge: ReturnType<typeof spawnBridge>; heartbeatTimer: NodeJS.Timeout } {
  const bridge = spawnBridge({
    accessToken,
    rpcPath: "/agent.v1.AgentService/Run",
  });
  bridge.write(frameConnectMessage(requestBytes));
  const heartbeatTimer = setInterval(() => bridge.write(makeHeartbeatBytes()), 5_000);
  return { bridge, heartbeatTimer };
}

function handleStreamingResponse(
  payload: CursorRequestPayload,
  accessToken: string,
  modelId: string,
  bridgeKey: string,
): Response {
  const { bridge, heartbeatTimer } = startBridge(accessToken, payload.requestBytes);
  return createBridgeStreamResponse(
    bridge, heartbeatTimer,
    payload.blobStore, payload.mcpTools,
    modelId, bridgeKey,
  );
}

/** Resume a paused bridge by sending MCP/shell results and continuing to stream. */
function handleToolResultResume(
  active: ActiveBridge,
  toolResults: ToolResultInfo[],
  modelId: string,
  bridgeKey: string,
): Response {
  const { bridge, heartbeatTimer, blobStore, mcpTools, pendingExecs } = active;

  const sendPendingResults = () => {
    for (const exec of pendingExecs) {
      const result = toolResults.find((r) => {
        const id = sanitizeToolCallId(r.toolCallId);
        return id === exec.toolCallId || r.toolCallId === exec.toolCallId;
      });

      if (exec.resumeKind === "shell" || exec.resumeKind === "shellStream") {
        const output = (() => {
          const raw = result?.content ?? "";
          const marker = "\nOutput:\n";
          const idx = raw.indexOf(marker);
          return idx >= 0 ? raw.slice(idx + marker.length) : raw;
        })();
        const execRef = { id: exec.execMsgId, execId: exec.execId } as ExecServerMessage;
        try {
          appendFileSync(
            "/tmp/cursor-bridge-debug.log",
            `[${new Date().toISOString()}] resume ${exec.resumeKind} id=${exec.toolCallId} matched=${Boolean(result)} out=${JSON.stringify(output).slice(0, 120)}\n`,
          );
        } catch {}
        if (exec.resumeKind === "shellStream") {
          if (result) {
            sendExecResult(
              execRef,
              "shellStream",
              create(ShellStreamSchema, {
                event: {
                  case: "start",
                  value: create(ShellStreamStartSchema, {}),
                },
              }),
              (data) => bridge.write(data),
            );
            sendExecResult(
              execRef,
              "shellStream",
              create(ShellStreamSchema, {
                event: {
                  case: "stdout",
                  value: create(ShellStreamStdoutSchema, { data: output }),
                },
              }),
              (data) => bridge.write(data),
            );
            sendExecResult(
              execRef,
              "shellStream",
              create(ShellStreamSchema, {
                event: {
                  case: "exit",
                  value: create(ShellStreamExitSchema, {
                    code: 0,
                    cwd: exec.shellCwd ?? "",
                    aborted: false,
                  }),
                },
              }),
              (data) => bridge.write(data),
            );
          } else {
            sendExecResult(
              execRef,
              "shellStream",
              create(ShellStreamSchema, {
                event: {
                  case: "exit",
                  value: create(ShellStreamExitSchema, {
                    code: 1,
                    cwd: exec.shellCwd ?? "",
                    aborted: true,
                  }),
                },
              }),
              (data) => bridge.write(data),
            );
          }
          continue;
        }
        const shellResult = result
          ? create(ShellResultSchema, {
              result: {
                case: "success",
                value: create(ShellSuccessSchema, {
                  command: exec.shellCommand ?? "",
                  workingDirectory: exec.shellCwd ?? "",
                  exitCode: 0,
                  signal: "",
                  stdout: output,
                  stderr: "",
                  executionTime: 0,
                }),
              },
            })
          : create(ShellResultSchema, {
              result: {
                case: "rejected",
                value: create(ShellRejectedSchema, {
                  command: exec.shellCommand ?? "",
                  workingDirectory: exec.shellCwd ?? "",
                  reason: "Tool result not provided",
                  isReadonly: false,
                }),
              },
            });
        sendExecResult(execRef, "shellResult", shellResult, (data) => bridge.write(data));
        continue;
      }

      const mcpResult = result
        ? create(McpResultSchema, {
            result: {
              case: "success",
              value: create(McpSuccessSchema, {
                content: [
                  create(McpToolResultContentItemSchema, {
                    content: {
                      case: "text",
                      value: create(McpTextContentSchema, { text: result.content }),
                    },
                  }),
                ],
                isError: false,
              }),
            },
          })
        : create(McpResultSchema, {
            result: {
              case: "error",
              value: create(McpErrorSchema, { error: "Tool result not provided" }),
            },
          });
      try {
        appendFileSync(
          "/tmp/cursor-bridge-debug.log",
          `[${new Date().toISOString()}] resume mcp id=${exec.toolCallId} name=${exec.toolName} matched=${Boolean(result)} out=${JSON.stringify(result?.content ?? "").slice(0, 120)}\n`,
        );
      } catch {}

      const execClientMessage = create(ExecClientMessageSchema, {
        id: exec.execMsgId,
        execId: exec.execId,
        message: {
          case: "mcpResult" as any,
          value: mcpResult as any,
        },
      });

      const clientMessage = create(AgentClientMessageSchema, {
        message: { case: "execClientMessage", value: execClientMessage },
      });

      bridge.write(
        frameConnectMessage(toBinary(AgentClientMessageSchema, clientMessage)),
      );
    }
  };

  return createBridgeStreamResponse(
    bridge, heartbeatTimer,
    blobStore, mcpTools,
    modelId, bridgeKey,
    sendPendingResults,
  );
}

async function handleNonStreamingResponse(
  payload: CursorRequestPayload,
  accessToken: string,
  modelId: string,
  bridgeKey: string,
): Promise<Response> {
  const completionId = `chatcmpl-${crypto.randomUUID().replace(/-/g, "").slice(0, 28)}`;
  const created = Math.floor(Date.now() / 1000);

  try {
    const fullText = await collectFullResponse(payload, accessToken, bridgeKey);
    return new Response(
      JSON.stringify({
        id: completionId,
        object: "chat.completion",
        created,
        model: modelId,
        choices: [
          {
            index: 0,
            message: { role: "assistant", content: fullText },
            finish_reason: "stop",
          },
        ],
        usage: {
          prompt_tokens: 0,
          completion_tokens: 0,
          total_tokens: 0,
        },
      }),
      { headers: { "Content-Type": "application/json" } },
    );
  } catch (err) {
    const message = err instanceof Error ? err.message : String(err);
    const code = err && typeof err === "object" && "code" in err
      ? String((err as { code?: string }).code ?? "")
      : "";
    return new Response(
      JSON.stringify(openAIErrorPayload(message, code)),
      {
        status: connectErrorHttpStatus(code, message),
        headers: { "Content-Type": "application/json" },
      },
    );
  }
}

async function collectFullResponse(
  payload: CursorRequestPayload,
  accessToken: string,
  bridgeKey: string,
): Promise<string> {
  const { promise, resolve, reject } = Promise.withResolvers<string>();
  let fullText = "";
  let streamError: { code: string; message: string } | null = null;

  const { bridge, heartbeatTimer } = startBridge(accessToken, payload.requestBytes);

  const state: StreamState = {
    toolCallIndex: 0,
    pendingExecs: [],
  };
  const tagFilter = createThinkingTagFilter();

  bridge.onData(createConnectFrameParser(
    (messageBytes) => {
      try {
        const serverMessage = fromBinary(
          AgentServerMessageSchema,
          messageBytes,
        );
        processServerMessage(
          serverMessage,
          payload.blobStore,
          payload.mcpTools,
          (data) => bridge.write(data),
          state,
          (text, isThinking) => {
            if (isThinking) return;
            const { content } = tagFilter.process(text);
            fullText += content;
          },
          () => {},
          (checkpointBytes) => {
            const stored = conversationStates.get(bridgeKey);
            if (stored) {
              stored.checkpoint = checkpointBytes;
              stored.lastAccessMs = Date.now();
            }
          },
        );
      } catch {
        // Skip
      }
    },
    (endStreamBytes) => {
      const endError = parseConnectEndStream(endStreamBytes);
      if (endError) streamError = endError;
    },
  ));

  bridge.onClose(() => {
    clearInterval(heartbeatTimer);
    const stored = conversationStates.get(bridgeKey);
    if (stored) {
      for (const [k, v] of payload.blobStore) stored.blobStore.set(k, v);
      stored.lastAccessMs = Date.now();
    }
    if (streamError) {
      const err = new Error(streamError.message) as Error & { code?: string };
      err.code = streamError.code;
      reject(err);
      return;
    }
    const flushed = tagFilter.flush();
    fullText += flushed.content;
    resolve(fullText);
  });

  return promise;
}

async function handleImageGenerations(
  body: ImageGenerationRequest,
  accessToken: string,
): Promise<Response> {
  const prompt = body.prompt?.trim();
  if (!prompt) {
    return new Response(
      JSON.stringify({
        error: { message: "prompt is required", type: "invalid_request_error" },
      }),
      { status: 400, headers: { "Content-Type": "application/json" } },
    );
  }

  const requestedModelId = body.model?.trim() || "gpt-5.3-codex";
  const modelId = resolveCursorModelId(requestedModelId, proxyModels);
  const bridgeKey = createHash("sha256")
    .update(`image:${modelId}:${prompt.slice(0, 200)}`)
    .digest("hex")
    .slice(0, 16);
  const systemPrompt = "You must generate an image for the user request using the built-in image generation tool. Do not answer with text only.";
  const userText = `Generate an image: ${prompt}`;
  const payload = buildCursorRequest(
    modelId,
    systemPrompt,
    userText,
    [],
    crypto.randomUUID(),
    null,
  );

  try {
    const imageData = await collectImageResponse(payload, accessToken, bridgeKey);
    const responseFormat = body.response_format ?? "b64_json";
    const dataItem = responseFormat === "url"
      ? { url: `data:image/png;base64,${imageData}`, revised_prompt: prompt }
      : { b64_json: imageData, revised_prompt: prompt };
    return new Response(
      JSON.stringify({
        created: Math.floor(Date.now() / 1000),
        data: [dataItem],
      }),
      { headers: { "Content-Type": "application/json" } },
    );
  } catch (err) {
    const message = err instanceof Error ? err.message : String(err);
    return new Response(
      JSON.stringify({
        error: { message, type: "server_error", code: "image_generation_failed" },
      }),
      { status: 502, headers: { "Content-Type": "application/json" } },
    );
  }
}

async function collectImageResponse(
  payload: CursorRequestPayload,
  accessToken: string,
  bridgeKey: string,
): Promise<string> {
  const { promise, resolve, reject } = Promise.withResolvers<string>();
  let settled = false;
  const finish = (imageData: string) => {
    if (settled) return;
    settled = true;
    resolve(imageData);
  };
  const fail = (message: string) => {
    if (settled) return;
    settled = true;
    reject(new Error(message));
  };

  const { bridge, heartbeatTimer } = startBridge(accessToken, payload.requestBytes);
  const timeout = setTimeout(() => {
    fail("image generation timed out");
    clearInterval(heartbeatTimer);
    try { bridge.proc.kill(); } catch {}
  }, 180_000);

  const state: StreamState = {
    toolCallIndex: 0,
    pendingExecs: [],
  };

  bridge.onData(createConnectFrameParser(
    (messageBytes) => {
      try {
        const serverMessage = fromBinary(
          AgentServerMessageSchema,
          messageBytes,
        );
        processServerMessage(
          serverMessage,
          payload.blobStore,
          payload.mcpTools,
          (data) => bridge.write(data),
          state,
          () => {},
          () => {},
          (checkpointBytes) => {
            const stored = conversationStates.get(bridgeKey);
            if (stored) {
              stored.checkpoint = checkpointBytes;
              stored.lastAccessMs = Date.now();
            }
          },
          (imageData) => {
            clearTimeout(timeout);
            clearInterval(heartbeatTimer);
            bridge.end();
            finish(imageData);
          },
        );
      } catch {
        // Skip unparseable messages
      }
    },
    (endStreamBytes) => {
      const endError = parseConnectEndStream(endStreamBytes);
      if (endError) fail(endError.message);
    },
  ));

  bridge.onClose((code) => {
    clearTimeout(timeout);
    clearInterval(heartbeatTimer);
    const stored = conversationStates.get(bridgeKey);
    if (stored) {
      for (const [k, v] of payload.blobStore) stored.blobStore.set(k, v);
      stored.lastAccessMs = Date.now();
    }
    if (!settled) {
      fail(code === 0
        ? "image generation ended without image data"
        : `bridge closed with code ${code}`);
    }
  });

  return promise;
}
