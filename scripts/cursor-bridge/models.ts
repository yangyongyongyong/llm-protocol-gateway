/**
 * Cursor model discovery.
 *
 * Primary source: AgentService.GetUsableModels on agent.api5.cursor.sh
 * using application/proto (NOT connect+proto — that returns HTTP 415).
 *
 * Secondary: AiService.AvailableModels on api2.cursor.sh (same catalog the
 * IDE model picker uses), merged when GetUsableModels is incomplete.
 *
 * Successful catalogs are persisted to disk so restarts keep newly shipped
 * models even if a transient discovery failure occurs. FALLBACK_MODELS is
 * only a last-resort bootstrap, not the way to add new models.
 */
import { create, fromBinary, toBinary } from "@bufbuild/protobuf";
import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { z } from "zod";
import { callCursorUnaryRpc } from "./proxy";
import {
  GetUsableModelsRequestSchema,
  GetUsableModelsResponseSchema,
} from "./proto/agent_pb";

const GET_USABLE_MODELS_PATH = "/agent.v1.AgentService/GetUsableModels";
const AVAILABLE_MODELS_PATH = "/aiserver.v1.AiService/AvailableModels";

/** Agent unary RPCs live on agent.api5; AiService stays on api2. */
const AGENT_API_URL =
  process.env.CURSOR_AGENT_API_URL ?? "https://agent.api5.cursor.sh";
const AISERVER_API_URL =
  process.env.CURSOR_API_URL ?? "https://api2.cursor.sh";

const DEFAULT_CONTEXT_WINDOW = 200_000;
const DEFAULT_MAX_TOKENS = 64_000;
const DISCOVERY_TIMEOUT_MS = 20_000;

/**
 * Known Cursor model id aliases.
 * Cursor AgentService currently rejects bare 4.6 / 5.6 ids with Connect
 * not_found; the working ids use the `-max` suffix.
 */
const CURSOR_MODEL_ALIASES: Record<string, string> = {
  "claude-4.6-sonnet": "claude-4.6-sonnet-max",
  "claude-4.6-opus": "claude-4.6-opus-max",
  "gpt-5.6-sol": "gpt-5.6-sol-max",
  "gpt-5.6-terra": "gpt-5.6-terra-max",
  "gpt-5.6-luna": "gpt-5.6-luna-max",
};

const CursorModelDetailsSchema = z.object({
  modelId: z.string(),
  displayName: z.string().optional().catch(undefined),
  displayNameShort: z.string().optional().catch(undefined),
  displayModelId: z.string().optional().catch(undefined),
  aliases: z
    .array(z.unknown())
    .optional()
    .catch([])
    .transform((aliases) =>
      (aliases ?? []).filter(
        (alias: unknown): alias is string => typeof alias === "string",
      ),
    ),
  thinkingDetails: z.unknown().optional(),
  maxMode: z.boolean().optional().catch(undefined),
});

type CursorModelDetails = z.infer<typeof CursorModelDetailsSchema>;

export interface CursorModel {
  id: string;
  name: string;
  reasoning: boolean;
  contextWindow: number;
  maxTokens: number;
}

/**
 * Minimal bootstrap only — used when live discovery AND the on-disk cache
 * both fail. Do NOT add newly shipped models here; fix discovery instead.
 */
const FALLBACK_MODELS: CursorModel[] = [
  { id: "composer-2", name: "Composer 2", reasoning: true, contextWindow: 200_000, maxTokens: 64_000 },
  { id: "composer-2-fast", name: "Composer 2 Fast", reasoning: true, contextWindow: 200_000, maxTokens: 64_000 },
  { id: "claude-4.6-sonnet-max", name: "Claude 4.6 Sonnet", reasoning: true, contextWindow: 200_000, maxTokens: 64_000 },
  { id: "claude-4.6-opus-max", name: "Claude 4.6 Opus", reasoning: true, contextWindow: 200_000, maxTokens: 128_000 },
];

/**
 * Resolve a client/requested model id to a Cursor-usable model id.
 * Prefers exact matches in the available catalog, then known aliases,
 * then a `-max` heuristic when that variant exists in the catalog.
 */
export function resolveCursorModelId(
  requested: string,
  available?: ReadonlyArray<{ id: string }> | null,
): string {
  const id = requested.trim();
  if (!id) return id;

  const catalog =
    available && available.length > 0
      ? available
      : (cachedModels ?? loadPersistedCatalog() ?? FALLBACK_MODELS);

  const byId = new Map<string, string>();
  for (const model of catalog) {
    const mid = model.id.trim();
    if (mid) byId.set(mid.toLowerCase(), mid);
  }

  // Known aliases take priority so friendly catalog entries still map to
  // the working Cursor id (e.g. claude-4.6-sonnet → claude-4.6-sonnet-max).
  const aliasTarget = CURSOR_MODEL_ALIASES[id] ?? CURSOR_MODEL_ALIASES[id.toLowerCase()];
  if (aliasTarget) {
    const resolvedAlias = byId.get(aliasTarget.toLowerCase());
    if (resolvedAlias) return resolvedAlias;
    return aliasTarget;
  }

  const exact = byId.get(id.toLowerCase());
  if (exact) return exact;

  // Heuristic: bare id → id-max when only the max variant is listed.
  if (!id.endsWith("-max")) {
    const maxVariant = byId.get(`${id.toLowerCase()}-max`);
    if (maxVariant) return maxVariant;
  }

  return id;
}

function catalogPath(): string | null {
  const tokenFile = process.env.CURSOR_TOKEN_FILE?.trim();
  if (!tokenFile) return null;
  return join(dirname(tokenFile), "models-catalog.json");
}

function loadPersistedCatalog(): CursorModel[] | null {
  const path = catalogPath();
  if (!path) return null;
  try {
    const raw = JSON.parse(readFileSync(path, "utf8")) as {
      models?: unknown;
    };
    if (!Array.isArray(raw.models)) return null;
    const models = raw.models
      .map((entry) => {
        if (!entry || typeof entry !== "object") return null;
        const m = entry as Record<string, unknown>;
        const id = typeof m.id === "string" ? m.id.trim() : "";
        if (!id) return null;
        return {
          id,
          name: typeof m.name === "string" && m.name.trim() ? m.name.trim() : id,
          reasoning: Boolean(m.reasoning),
          contextWindow:
            typeof m.contextWindow === "number" && m.contextWindow > 0
              ? m.contextWindow
              : DEFAULT_CONTEXT_WINDOW,
          maxTokens:
            typeof m.maxTokens === "number" && m.maxTokens > 0
              ? m.maxTokens
              : DEFAULT_MAX_TOKENS,
        } satisfies CursorModel;
      })
      .filter((m): m is CursorModel => m !== null);
    return models.length > 0 ? models : null;
  } catch {
    return null;
  }
}

function persistCatalog(models: CursorModel[]): void {
  const path = catalogPath();
  if (!path || models.length === 0) return;
  try {
    mkdirSync(dirname(path), { recursive: true, mode: 0o700 });
    writeFileSync(
      path,
      JSON.stringify(
        {
          updatedAt: new Date().toISOString(),
          source: "cursor-discovery",
          models,
        },
        null,
        2,
      ),
      { encoding: "utf8", mode: 0o600 },
    );
  } catch (err) {
    console.warn(
      "[cursor-bridge] failed to persist model catalog:",
      err instanceof Error ? err.message : String(err),
    );
  }
}

function logDiscovery(
  source: string,
  models: CursorModel[],
  extra?: Record<string, unknown>,
): void {
  const ids = models.map((m) => m.id);
  const sample = ids.slice(0, 40);
  console.log(
    JSON.stringify({
      event: "model_discovery",
      source,
      count: models.length,
      hasGpt56: ids.some((id) => id.includes("gpt-5.6")),
      sample,
      ...extra,
    }),
  );
}

async function fetchCursorUsableModels(
  apiKey: string,
): Promise<CursorModel[] | null> {
  try {
    const requestPayload = create(GetUsableModelsRequestSchema, {});
    const requestBody = toBinary(GetUsableModelsRequestSchema, requestPayload);

    const response = await callCursorUnaryRpc({
      accessToken: apiKey,
      rpcPath: GET_USABLE_MODELS_PATH,
      requestBody,
      url: AGENT_API_URL,
      transport: "proto",
      timeoutMs: DISCOVERY_TIMEOUT_MS,
    });

    if (response.timedOut || response.exitCode !== 0 || response.body.length === 0) {
      console.log(
        JSON.stringify({
          event: "model_discovery",
          source: "GetUsableModels",
          ok: false,
          timedOut: response.timedOut,
          exitCode: response.exitCode,
          bodyLen: response.body.length,
        }),
      );
      return null;
    }

    const decoded = decodeGetUsableModelsResponse(response.body);
    if (!decoded) {
      console.log(
        JSON.stringify({
          event: "model_discovery",
          source: "GetUsableModels",
          ok: false,
          reason: "decode_failed",
          bodyLen: response.body.length,
        }),
      );
      return null;
    }

    const models = normalizeCursorModels(decoded.models);
    logDiscovery("GetUsableModels", models, { ok: true });
    return models.length > 0 ? models : null;
  } catch (err) {
    console.log(
      JSON.stringify({
        event: "model_discovery",
        source: "GetUsableModels",
        ok: false,
        reason: err instanceof Error ? err.message : String(err),
      }),
    );
    return null;
  }
}

/**
 * IDE model-picker catalog (aiserver.v1.AiService/AvailableModels).
 * Used as a merge source when GetUsableModels is empty/incomplete.
 */
async function fetchAvailableModels(
  apiKey: string,
): Promise<CursorModel[] | null> {
  try {
    // AvailableModelsRequest: include_long_context=true, exclude_max_named=false,
    // include_hidden=true, variants_will_be_shown_in_exploded_list=true
    const requestBody = encodeAvailableModelsRequest({
      includeLongContext: true,
      excludeMaxNamed: false,
      includeHidden: true,
      explodedVariants: true,
    });

    const response = await callCursorUnaryRpc({
      accessToken: apiKey,
      rpcPath: AVAILABLE_MODELS_PATH,
      requestBody,
      url: AISERVER_API_URL,
      transport: "proto",
      clientType: "ide",
      clientVersion: "1.7.44",
      timeoutMs: DISCOVERY_TIMEOUT_MS,
    });

    if (response.timedOut || response.exitCode !== 0 || response.body.length === 0) {
      console.log(
        JSON.stringify({
          event: "model_discovery",
          source: "AvailableModels",
          ok: false,
          timedOut: response.timedOut,
          exitCode: response.exitCode,
          bodyLen: response.body.length,
        }),
      );
      return null;
    }

    const models = decodeAvailableModelsResponse(response.body);
    logDiscovery("AvailableModels", models, { ok: true });
    return models.length > 0 ? models : null;
  } catch (err) {
    console.log(
      JSON.stringify({
        event: "model_discovery",
        source: "AvailableModels",
        ok: false,
        reason: err instanceof Error ? err.message : String(err),
      }),
    );
    return null;
  }
}

let cachedModels: CursorModel[] | null = null;
let cachedAtMs = 0;
/** In-memory catalog TTL; /v1/models refreshes when stale. */
const CATALOG_TTL_MS = 5 * 60 * 1000;

export async function getCursorModels(
  apiKey: string,
  options?: { force?: boolean },
): Promise<CursorModel[]> {
  const force = options?.force === true;
  if (
    !force &&
    cachedModels &&
    Date.now() - cachedAtMs < CATALOG_TTL_MS
  ) {
    return cachedModels;
  }

  const usable = await fetchCursorUsableModels(apiKey);
  let discovered = usable;

  // If GetUsableModels failed or looks suspiciously small, also pull the
  // IDE AvailableModels catalog and union them.
  const needsAvailable =
    !usable ||
    usable.length < 10 ||
    !usable.some((m) => m.id.includes("gpt-5.6") || m.id.includes("claude-4.6"));

  if (needsAvailable) {
    const available = await fetchAvailableModels(apiKey);
    if (available) {
      discovered = mergeModelCatalogs(usable ?? [], available);
    }
  }

  const persisted = loadPersistedCatalog();
  // Live discovery wins; persisted fills gaps across restarts; FALLBACK last.
  const base = mergeModelCatalogs(
    mergeModelCatalogs(discovered ?? [], persisted ?? []),
    FALLBACK_MODELS,
  );
  cachedModels = withFriendlyAliases(base);
  cachedAtMs = Date.now();

  if (discovered && discovered.length > 0) {
    persistCatalog(cachedModels);
  }

  console.log(
    JSON.stringify({
      event: "model_catalog_ready",
      count: cachedModels.length,
      fromDiscovery: discovered?.length ?? 0,
      fromPersisted: persisted?.length ?? 0,
      hasGpt56: cachedModels.some((m) => m.id.includes("gpt-5.6")),
    }),
  );

  return cachedModels;
}

/** Discovered models win on id collision; secondary fills missing ids. */
function mergeModelCatalogs(
  primary: CursorModel[],
  secondary: CursorModel[],
): CursorModel[] {
  const byId = new Map<string, CursorModel>();
  for (const model of secondary) {
    byId.set(model.id, model);
  }
  for (const model of primary) {
    byId.set(model.id, model);
  }
  return [...byId.values()].sort((a, b) => a.id.localeCompare(b.id));
}

/** Force the next getCursorModels call to re-query Cursor. */
export function clearModelCache(): void {
  cachedModels = null;
  cachedAtMs = 0;
}

/** Ensure friendly aliases appear in the catalog when their targets exist. */
function withFriendlyAliases(models: CursorModel[]): CursorModel[] {
  const byId = new Map(models.map((m) => [m.id, m]));
  for (const [alias, target] of Object.entries(CURSOR_MODEL_ALIASES)) {
    if (byId.has(alias)) continue;
    const targetModel = byId.get(target);
    if (!targetModel) continue;
    byId.set(alias, {
      ...targetModel,
      id: alias,
      name: targetModel.name,
    });
  }
  return [...byId.values()].sort((a, b) => a.id.localeCompare(b.id));
}

function decodeGetUsableModelsResponse(payload: Uint8Array): {
  models: readonly unknown[];
} | null {
  try {
    return fromBinary(GetUsableModelsResponseSchema, payload);
  } catch {
    const framedBody = decodeConnectUnaryBody(payload);
    if (!framedBody) return null;
    try {
      return fromBinary(GetUsableModelsResponseSchema, framedBody);
    } catch {
      return null;
    }
  }
}

function decodeConnectUnaryBody(payload: Uint8Array): Uint8Array | null {
  if (payload.length < 5) return null;

  let offset = 0;
  while (offset + 5 <= payload.length) {
    const flags = payload[offset]!;
    const view = new DataView(
      payload.buffer,
      payload.byteOffset + offset,
      payload.byteLength - offset,
    );
    const messageLength = view.getUint32(1, false);
    const frameEnd = offset + 5 + messageLength;
    if (frameEnd > payload.length) return null;

    // Compression flag
    if ((flags & 0b0000_0001) !== 0) return null;

    // End-of-stream flag — skip trailer frames
    if ((flags & 0b0000_0010) === 0) {
      return payload.subarray(offset + 5, frameEnd);
    }

    offset = frameEnd;
  }

  return null;
}

function normalizeCursorModels(
  models: readonly unknown[],
): CursorModel[] {
  if (models.length === 0) return [];

  const byId = new Map<string, CursorModel>();
  for (const model of models) {
    const normalized = normalizeSingleModel(model);
    if (normalized) byId.set(normalized.id, normalized);
  }
  return [...byId.values()].sort((a, b) => a.id.localeCompare(b.id));
}

function normalizeSingleModel(model: unknown): CursorModel | null {
  const parsed = CursorModelDetailsSchema.safeParse(model);
  if (!parsed.success) return null;

  const details = parsed.data;
  // Prefer modelId; displayModelId is usually the same, but keep as fallback.
  const id = (details.modelId || details.displayModelId || "").trim();
  if (!id || id === "default") return null;

  return {
    id,
    name: pickDisplayName(details, id),
    reasoning: Boolean(details.thinkingDetails),
    contextWindow: DEFAULT_CONTEXT_WINDOW,
    maxTokens: DEFAULT_MAX_TOKENS,
  };
}

function pickDisplayName(model: CursorModelDetails, fallbackId: string): string {
  const candidates = [
    model.displayName,
    model.displayNameShort,
    model.displayModelId,
    ...model.aliases,
    fallbackId,
  ];
  for (const candidate of candidates) {
    if (typeof candidate !== "string") continue;
    const trimmed = candidate.trim();
    if (trimmed) return trimmed;
  }
  return fallbackId;
}

// --- Minimal protobuf helpers for AvailableModels (no generated schema) ---

function encodeVarint(n: number): Uint8Array {
  const bytes: number[] = [];
  let v = n >>> 0;
  while (v > 0x7f) {
    bytes.push((v & 0x7f) | 0x80);
    v >>>= 7;
  }
  bytes.push(v);
  return Uint8Array.from(bytes);
}

function encodeTag(field: number, wire: number): Uint8Array {
  return encodeVarint((field << 3) | wire);
}

function encodeBoolField(field: number, value: boolean): Uint8Array {
  return Uint8Array.from([
    ...encodeTag(field, 0),
    ...encodeVarint(value ? 1 : 0),
  ]);
}

function encodeAvailableModelsRequest(opts: {
  includeLongContext: boolean;
  excludeMaxNamed: boolean;
  includeHidden: boolean;
  explodedVariants: boolean;
}): Uint8Array {
  return Uint8Array.from([
    ...encodeBoolField(2, opts.includeLongContext),
    ...encodeBoolField(3, opts.excludeMaxNamed),
    ...encodeBoolField(6, opts.includeHidden),
    ...encodeBoolField(8, opts.explodedVariants),
  ]);
}

function readVarint(buf: Uint8Array, offset: number): [number, number] {
  let n = 0;
  let shift = 0;
  let pos = offset;
  while (pos < buf.length) {
    const b = buf[pos++]!;
    n |= (b & 0x7f) << shift;
    if ((b & 0x80) === 0) break;
    shift += 7;
    if (shift > 35) break;
  }
  return [n >>> 0, pos];
}

function decodeAvailableModelsResponse(buf: Uint8Array): CursorModel[] {
  const byId = new Map<string, CursorModel>();
  let pos = 0;
  while (pos < buf.length) {
    const [tag, p1] = readVarint(buf, pos);
    pos = p1;
    const field = tag >>> 3;
    const wire = tag & 7;
    if (wire === 2) {
      const [len, p2] = readVarint(buf, pos);
      pos = p2;
      const slice = buf.subarray(pos, pos + len);
      pos += len;
      if (field === 2) {
        const model = parseAvailableModelMessage(slice);
        if (model) byId.set(model.id, model);
      } else if (field === 1) {
        const name = new TextDecoder().decode(slice).trim();
        if (name && name !== "default" && !byId.has(name)) {
          byId.set(name, {
            id: name,
            name,
            reasoning: false,
            contextWindow: DEFAULT_CONTEXT_WINDOW,
            maxTokens: DEFAULT_MAX_TOKENS,
          });
        }
      }
    } else if (wire === 0) {
      const [, p2] = readVarint(buf, pos);
      pos = p2;
    } else if (wire === 5) {
      pos += 4;
    } else if (wire === 1) {
      pos += 8;
    } else {
      break;
    }
  }
  return [...byId.values()].sort((a, b) => a.id.localeCompare(b.id));
}

function parseAvailableModelMessage(buf: Uint8Array): CursorModel | null {
  let name = "";
  let clientDisplayName = "";
  let serverModelName = "";
  let supportsThinking = false;
  let supportsAgent = false;
  let context = 0;
  let contextMax = 0;
  let pos = 0;
  while (pos < buf.length) {
    const [tag, p1] = readVarint(buf, pos);
    pos = p1;
    const field = tag >>> 3;
    const wire = tag & 7;
    if (wire === 2) {
      const [len, p2] = readVarint(buf, pos);
      pos = p2;
      const slice = buf.subarray(pos, pos + len);
      pos += len;
      const s = new TextDecoder().decode(slice);
      if (field === 1) name = s;
      else if (field === 17) clientDisplayName = s;
      else if (field === 18) serverModelName = s;
    } else if (wire === 0) {
      const [v, p2] = readVarint(buf, pos);
      pos = p2;
      if (field === 5) supportsAgent = !!v;
      else if (field === 9) supportsThinking = !!v;
      else if (field === 15) context = v;
      else if (field === 16) contextMax = v;
    } else if (wire === 5) {
      pos += 4;
    } else if (wire === 1) {
      pos += 8;
    } else {
      break;
    }
  }

  const id = (serverModelName || name).trim();
  if (!id || id === "default") return null;
  // Prefer agent-capable models for the bridge catalog.
  if (!supportsAgent && !id.includes("composer")) return null;

  return {
    id,
    name: (clientDisplayName || name || id).trim(),
    reasoning: supportsThinking,
    contextWindow: contextMax || context || DEFAULT_CONTEXT_WINDOW,
    maxTokens: DEFAULT_MAX_TOKENS,
  };
}
