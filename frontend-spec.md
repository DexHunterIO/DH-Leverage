# DH-Leverage — Frontend Integration Spec

Written for the team building the Milestone 4 frontend. It describes every HTTP
route the Go backend (`go run main.go api`) exposes, the exact request and
response shapes, and the CIP-30 wallet steps the browser has to perform between
calls. The embedded test UI in `web/index.html` is a working reference
implementation of every flow below.

---

## 1. Conventions

| Topic | Rule |
|---|---|
| Base URL | `http://<host>:8080` by default (`API_PORT`). All routes live under `/api`. |
| Transport | JSON over HTTP. `Content-Type: application/json` on every POST. |
| Auth | None. The user is identified by the wallet address they pass in each request. Strike routes additionally require a one-time "connect" handshake (section 6). |
| Errors | Non-2xx responses carry `{ "error": "<message>" }`. Two exceptions: `/api/tx/finalize` returns a plain-text body on failure, and Strike pass-through routes may return Strike's own JSON on 2xx with an `error` field inside, so check both `r.ok` and `body.error`. |
| Status codes | `400` bad input, `401` Strike not connected for this address, `404` unknown source/job, `501` source can't build txs, `502` upstream protocol failed, `503` feature disabled by server config. |
| Timeouts | Server read/write timeout is 30 s. Handlers use 15–45 s upstream budgets. Expect slow responses on tx build endpoints. |
| Amounts | All user-facing amounts are **whole units** (`100` = 100 ADA, `1500` = 1500 SNEK). The backend converts to lovelace / raw units. Strike routes are the exception and take strings (see section 6). |
| Asset identity | A "unit" is `policyId + assetNameHex` concatenated. ADA is the empty string `""`. The `Asset` object form is `{policyId, assetName, symbol, decimals}`. |
| Sources | `"liqwid"` and `"surf"`. These are the `source` values accepted everywhere. Levvy has no public API and is not wired. |
| CORS | **Not configured.** The backend serves the frontend from the same origin and has no CORS middleware. A separately hosted frontend must either be reverse-proxied under the same origin or CORS must be added to `common/api/server.go` before launch. |

---

## 2. Wallet connection (CIP-30) — what the frontend must derive

The backend never talks to the wallet. After `window.cardano.<key>.enable()` the
frontend derives and keeps the following, because different routes want
different representations of the same wallet:

| Value | How to get it | Used by |
|---|---|---|
| `walletKey` | the `window.cardano` key (`"lace"`, `"eternl"`, `"nami"`, …) | `wallet` field on tx build/submit (Surf uses it as its `canonical` flag) |
| `address` (bech32) | decode `getUsedAddresses()[0]` (fallback `getUnusedAddresses()[0]`) from CBOR-hex to bech32 | almost everything: markets orders (Surf), tx builders, leverage `owner`, Strike `address` |
| `changeAddress` (bech32) | decode `getChangeAddress()` | tx builders, leverage open |
| `otherAddresses` (bech32[]) | remaining `getUsedAddresses()` decoded | tx builders (coin selection) |
| `pkh` (hex) | payment key hash extracted from the address bytes | `/api/orders?source=liqwid&address=<pkh>`, `pkh` on `/api/tx/close` for Liqwid |
| `addressHex` (raw bytes hex) | strip the CBOR bytestring header from the hex `getUsedAddresses()` returns | first argument to `signData()` for Strike |
| `utxos` (cbor hex[]) | `getUtxos()` right before each build call | tx builders, leverage open, Strike deposit/withdraw |

Rules that bite:

- Check `getNetworkId()` is `1` (mainnet) or `0` (testnet). Lace exposes a Midnight context under the same namespace that returns other ids.
- Always call `signTx(cbor, true)` (partial = true). It returns a **witness set**, not a signed tx. Send that witness set to the backend; never merge CBOR in the browser.
- Liqwid refuses to build with zero UTxOs. Surface a "wallet locked / wrong network / permission revoked" error if `getUtxos()` is empty.

---

## 3. Read endpoints

### `GET /api/health`
`{ "status": "ok" }`

### `GET /api/markets` · `GET /api/markets/by-token/:id`
Query: `source=liqwid|surf` (optional), `token=<symbol|policyId|unit>` (optional, case-insensitive; `ada` matches ADA).

```json
{
  "fetchedAt": "2026-10-06T10:00:00Z",
  "sources": { "liqwid": "", "surf": "upstream 502" },
  "markets": [ Market, ... ]
}
```
`sources` maps each source to its error string, empty when healthy. A failing source never blanks the response, so render partial data and show the error per source.

`Market`:
```ts
{
  source: "liqwid" | "surf";
  poolId: string;                  // protocol-internal id → marketId in tx calls
  collateral: Asset;               // zero-valued for Liqwid (any supplied asset can be collateral)
  borrow: Asset;
  receiptAsset?: Asset;            // qToken/fToken minted to suppliers
  collateralId?: string;           // Liqwid only
  supplyExchangeRate?: number;     // underlying = receiptQty * supplyExchangeRate
  totalSuppliedUsd: number; totalBorrowedUsd: number; availableLiquidityUsd: number;
  supplyApy: number; borrowApy: number;     // percent
  ltv: number; liquidationThreshold: number; // 0..1
  minSupply?: number;              // whole units; 0 = none
  active: boolean;
  url?: string;
}
```
To show the user's **supply positions** without a dedicated endpoint, scan the wallet balance for each market's `receiptAsset` and convert with `supplyExchangeRate`.

### `GET /api/orders`
Query: `source`, `address`, `limit`, `refresh=1` (bypass the 30 s cache after a submit).

**Address semantics differ by source.** Liqwid wants the payment key hash hex; Surf wants the bech32 address. Query each source separately:
```
/api/orders?source=liqwid&address=<pkh>&limit=100
/api/orders?source=surf&address=<bech32>&limit=100
```
Response: `{ fetchedAt, sources, orders: Order[] }`.

`Order`:
```ts
{
  source: string; id: string;
  type: "borrow"|"repay"|"repayWithCollateral"|"lend"|"deposit"|"withdraw"|"cancelWithdraw"|"leveragedBorrow"|"liquidation"|"cancel"|"unknown";
  status: "active"|"closed"|"pending";
  owner: string; marketId?: string;
  asset: Asset; amount: number; amountUsd?: number;
  collateralAsset?: Asset; collateralAmount?: number;
  interest?: number; apy?: number; ltv?: number; healthFactor?: number;
  txHash?: string; outputIndex?: number;   // outRef used by /api/tx/close
  timeMs: number;
}
```
Liqwid returns long-lived loans (`active`). Surf returns a settled activity log (`closed`) plus `active` positions and `pending` batcher orders. Only `active` and `pending` rows are closable.

### `GET /api/wallet/balance?address=<bech32>`
```ts
{ address: string; lovelace: number; tokens: { policyId, assetName, symbol, decimals, quantity: string, fingerprint? }[]; fetchedAt: string }
```
Cached 30 s per address. `quantity` is a raw-unit string.

---

## 4. Lend / borrow / withdraw / close (per-source, user-signed)

Every mutation is the same three-step dance. The backend builds an unsigned tx, the wallet adds witnesses, the backend submits through the protocol.

```
POST /api/tx/<action>  →  signTx(cbor, true)  →  POST /api/tx/submit
```

### 4.1 Build — `POST /api/tx/supply` · `/api/tx/withdraw` · `/api/tx/borrow`
Body (`TxParams`):
```ts
{
  source: "liqwid" | "surf";
  marketId: string;            // Market.poolId
  amount: number;              // whole units of the supply/borrow asset, > 0
  collateralAmount?: number;   // borrow only, whole units of the collateral asset
  collateralUnit?: string;     // borrow on Surf: which collateral ("" = ADA)
  full?: boolean;              // withdraw only: withdraw everything
  wallet: string; address: string; changeAddress: string;
  otherAddresses: string[]; utxos: string[];
}
```
Response (`BuiltTx`):
```ts
{ source, action, cbor: string, hint?: string, trackedOrder?: object, witnesses?: string[] }
```
`cbor` is the unsigned tx hex to sign. `hint` is a human description for the confirm modal. Pass `trackedOrder` and `witnesses` back untouched if present (Surf).

Errors: `400` validation, `404` unknown source, `501` source has no builder, `502` protocol rejected the build (message is the protocol's).

### 4.2 Close — `POST /api/tx/close`
Full repay of an active position or cancel of a pending order. No amount: the protocol computes it.
```ts
{
  source: string; marketId: string; address: string; wallet: string;
  txHash: string; outputIndex: number;   // from the Order row
  kind: "repay" | "cancel";              // default "repay"; Liqwid supports only "repay"
  redeemCollateral: boolean;             // Liqwid: release collateral (true) or keep it supplied
  changeAddress: string; otherAddresses: string[]; utxos: string[];
  pkh: string;                           // Liqwid needs it to find the loan
}
```
Returns a `BuiltTx`. Sign and submit exactly like 4.3.

### 4.3 Submit — `POST /api/tx/submit`
```ts
{ source: string; cbor: string /* unsigned, from build */; witnessSet: string /* signTx result */; address: string; wallet: string }
```
Response: `{ "txHash": string }`.
Surf returns the literal `"already-on-chain"` as `txHash` when a duplicate submit hits a tx that already landed. Treat it as success.

After a successful submit, refetch orders with `refresh=1` and the wallet balance after ~1.5 s.

### 4.4 Lower-level helpers (rarely needed)
- `POST /api/tx/finalize` `{cbor, witnessSet}` → `{cbor}` merges witnesses locally without submitting. Error body is plain text.
- `POST /api/tx/submit-raw` `{cbor, witnessSet?}` → `{txHash}` broadcasts a tx straight to chain, bypassing any protocol. Used by the leverage and Strike flows below. Returns `503` if the leverage engine is disabled.

---

## 5. Leverage engine (server-driven long/short, ≤ 1.8x)

The user funds a backend-held temp address once; the backend then loops trade → lend → borrow, and later unwinds and sweeps back. The frontend signs **one** tx (funding) and otherwise polls.

All routes return `503 { error }` when the engine is disabled (missing BlockFrost key, encryption key, or non-durable store on mainnet). Check `/api/leverage/quote` once at startup and hide the feature if it 503s.

### 5.1 Request shape (`OpenRequest`), shared by quote and open
```ts
{
  owner: string;            // user's bech32 address — sweep destination
  direction: "long" | "short";
  collateralUnit: string;   // base asset the user funds with; "" = ADA (currently the only tested base)
  targetUnit: string;       // policyId+assetNameHex of the token to long/short (e.g. SNEK)
  amount: number;           // whole units of collateralUnit
  leverage: number;         // requested; clamped to [1, 1.8]
  utxos?: string[];         // open only
  changeAddress?: string;   // open only
}
```

### 5.2 `POST /api/leverage/quote` (read-only)
```ts
{
  plan: Plan; borrowSource: string; borrowMarket: string; borrowApy: number;
  legLtv: number; loops: number; txCount: number;
  liqMovePct: number;       // adverse % move that liquidates (drop for long, rise for short)
  liqThreshold: number;     // 0..1
  feeReserveAda: number;    // ADA held for fees on top of capital (≥ 30)
  totalAdaNeeded: number;   // capital + fee reserve the wallet must hold — gate the Open button on this
  minSupply?: number; note?: string;   // show `note` as a warning when present
}
```
`Plan`:
```ts
{ direction, initialAmount, targetLeverage, achievedLeverage, legLtv, totalExposure, totalBorrowed,
  legs: { index, tradeInAda, supplyAmt, borrowAmt, cumExposure }[] }
```
`achievedLeverage` can be lower than requested for small capital because each leg must borrow ≥ 20 ADA.

### 5.3 Open flow
1. `getUtxos()` and `getChangeAddress()`; abort if there are no UTxOs.
2. `POST /api/leverage/open` with the request plus `utxos` and `changeAddress` → `{ jobId, tempAddress, fundingCbor }`. The job is now `awaiting_funding` and the backend already watches for the funding tx.
3. `signTx(fundingCbor, true)` → witness set.
4. `POST /api/tx/submit-raw` `{ cbor: fundingCbor, witnessSet }` → `{ txHash }`.
5. `POST /api/leverage/start` `{ jobId }` → `{ status: "running" }`. Returns immediately; the engine starts as soon as funding confirms, even if the tab closes. Allowed only from `awaiting_funding` or `failed`.
6. Poll `GET /api/leverage/status/:id` every ~8 s until status leaves `running`.

If the user rejects the funding signature the job stays `awaiting_funding` with nothing at risk. It can be funded later from the positions list or ignored.

### 5.4 `GET /api/leverage/status/:id` → `Job` · `GET /api/leverage/jobs?address=<bech32>` → `{ jobs: Job[] }`
```ts
{
  id: string; owner: string; direction: "long"|"short";
  collateralUnit: string; targetUnit: string; initialAmount: number; leverage: number;
  plan: Plan; tempAddress: string; fundingLovelace: number; feeReserveLovelace: number;
  status: "awaiting_funding"|"running"|"open"|"reversing"|"reversed"|"failed";
  steps: { kind: "trade"|"supply"|"borrow"|"repay"|"withdraw"|"sweep"|"leg"|"batch"|"status";
           source?, marketId?, txHash?, url?, amount?, note?, at: string }[];
  exposure: number; healthFactor: number;
  entryPrice?: number; currentPrice?: number; pnlPct?: number; liqThreshold?: number; healthUpdatedAt?: string;
  error?: string; createdAt: string; updatedAt: string;
}
```
`steps[].url` is a Cardanoscan link. Health fields refresh in the background while the job is `open`; `healthFactor <= 1` means liquidatable.

### 5.5 Status machine and which buttons to show

| Status | Meaning | Allowed actions |
|---|---|---|
| `awaiting_funding` | temp address created, funding tx not seen | Start (re-arm), Reverse |
| `running` | loop building the position | Reverse |
| `open` | position live, health monitored | Reverse (close position) |
| `reversing` | unwinding | none — poll |
| `reversed` | unwound and swept back | Sweep (if change is left at temp address) |
| `failed` | a leg failed; funds sit at temp address | Retry, Reverse, Sweep |

- `POST /api/leverage/reverse` `{jobId}` → `{status:"reversing"}`. Unwinds (sell/repay/withdraw) and sweeps everything to `owner`. `400` if already reversing.
- `POST /api/leverage/retry` `{jobId}` → `{status:"running"}`. Resumes a `failed` job from on-chain state. `400` for any other status.
- `POST /api/leverage/sweep` `{jobId}` → `{status:"sweeping"}`. Sends loose funds at the temp address to `owner` **without** unwinding. `400` while `running`/`reversing` or if a position is still active.

Keep polling `status/:id` after any of these until the status settles. The UI should also poll `jobs` while any job is non-terminal (`awaiting_funding`, `running`, `reversing`).

---

## 6. Strike Finance perpetuals

The backend proxies Strike v2 and holds a per-user API key after a one-time connect. **Credentials live in backend memory and are lost on restart**, so always consult `/api/strike/status` and re-run connect when `connected` is false. Any authenticated route returns `401` for an unconnected address.

Pass-through routes return Strike's JSON verbatim, in **snake_case**. Read both spellings defensively where noted.

### 6.1 Status and public reads
- `GET /api/strike/status?address=<bech32>` → `{ builderEnabled: boolean, feeBps: number, connected: boolean }`. `503` if Strike isn't configured at all. `builderEnabled=false` means read-only: hide trading, deposits and withdrawals.
- `GET /api/strike/markets` → Strike markets array (fields such as `symbol`, `base_asset`, `min_notional`, price).
- `GET /api/strike/positions?address=` → Strike positions for that on-chain address.
- `GET /api/strike/account?address=` → Strike account summary.
- `GET /api/strike/balances?address=` → connected user's balances (**401 if not connected**).
- `GET /api/strike/history?address=` → `{ entries: HistoryEntry[] }`, newest first. Backend-recorded deposits/withdrawals:
  ```ts
  { id, address, type: "deposit"|"withdraw", asset, amount, usdValue, txHash, withdrawId, requestId,
    status: "confirmed"|"pending"|"sent"|"failed", createdAt }
  ```

### 6.2 Connect (once per address, per backend lifetime)
1. `POST /api/strike/connect/challenge` `{ address }` → `{ nonce, messageToSign }`. `503` if no builder code is configured.
2. `signData(addressHex, hex(messageToSign))` — raw address bytes hex as the first argument, UTF-8→hex of the message as the payload.
3. `POST /api/strike/connect/verify` `{ address, nonce, walletSignature: `${sig.signature}:${sig.key}` }` → `{ connected: true, accountId }`. `400` if the nonce expired: restart from step 1.

### 6.3 Leverage and orders (authenticated)
- `POST /api/strike/leverage` `{ address, symbol, leverage: number /* integer > 0 */ }` → Strike response.
- `POST /api/strike/order`
  ```ts
  { address, symbol, position: "Long"|"Short", side?: "buy"|"sell", type?: "market"|"limit", size: string, price?: string, reduceOnly?: boolean }
  ```
  `position` maps to `side` (Long→buy, Short→sell) unless `side` is given. `type` defaults to `market`. `reduceOnly: true` closes. Validate `size * price >= market.min_notional` client-side; Strike rejects below it.
- `POST /api/strike/order/cancel` `{ address, orderId: number, symbol }`.

### 6.4 Deposit ADA (quote → build → sign → submit-raw → confirm)
1. `POST /api/strike/deposit/quote` `{ address, lovelace: string }` → read `request_id` (or `requestId`) and `usd_value`.
2. `POST /api/strike/deposit/build` `{ address, requestId, userAddress: address, utxos }` → read `unsigned_tx` (or `cbor`).
3. `signTx(unsigned_tx, true)` → `POST /api/tx/submit-raw` `{ cbor, witnessSet }` → `{ txHash }`.
4. `POST /api/strike/deposit/confirm` `{ address, requestId, txHash, adaAmount: string, usdValue: string }`. The last two are display-only and feed `/history`.
Credits appear after confirmations; refresh balances later.

### 6.5 Withdraw (quote → signData → confirm → batcher tx → sign → submit-raw → settle)
1. `POST /api/strike/withdraw/quote` `{ address, usdValue: string, asset?: "USDM" }` → read `withdraw_id`, `fee`, `message_to_sign`. ADA is rejected as a settlement asset; default is USDM.
2. `signData(addressHex, message_to_sign)` — **pass the raw string, not hex-encoded**. This differs from connect and is required for Strike's verification to pass.
3. `POST /api/strike/withdraw/confirm` `{ address, withdrawId, walletSignature: JSON.stringify({ signature: sig.signature, key: sig.key }), asset, usdValue }`. Note the signature is a **stringified JSON object** here, not the colon-joined pair used by connect. The backend records a `pending` history entry.
4. `POST /api/strike/withdraw/batcher` `{ address, utxos, leaderAddress? }` → read `txData` (or `cbor`/`tx`). `400` if no leader is configured server-side and none is passed.
5. `signTx(txData, true)` → `POST /api/tx/submit-raw` → `{ txHash }`.
6. `POST /api/strike/withdraw/settle` `{ address, withdrawId, txHash, status: "sent" }` → `{ ok: true }` to attach the settlement hash to history. Fire-and-forget.

---

## 7. Polling and caching summary

| Data | Backend cache | Suggested client refresh |
|---|---|---|
| markets | 60 s (`MARKETS_CACHE_TTL`) | 60 s |
| orders | 30 s per (address, limit); `refresh=1` bypasses | after each submit with `refresh=1`, otherwise 30 s |
| wallet balance | 30 s | after each submit (~1.5 s delay) |
| leverage status/jobs | none | 8 s while any job is non-terminal |
| Strike positions/balances | none | after each order (~1.5 s delay), otherwise 15–30 s |

---

## 8. Error handling checklist

- Read `error` from the JSON body on every non-2xx; fall back to the HTTP status.
- `503` on `/api/leverage/*` or `/api/strike/*`: the feature is off for this deployment. Hide it and show the message to operators, not users.
- `401` on Strike: run the connect flow, then retry the original call.
- `502`: upstream protocol error. The message is usually the protocol's own text (e.g. a Liqwid GraphQL error). Show it verbatim in the modal.
- Wallet rejections surface as thrown CIP-30 errors from `signTx` / `signData`, not as HTTP errors. Catch them separately so a cancelled signature isn't reported as a backend failure.
- Never retry a `POST /api/tx/submit` blindly; a duplicate on Surf returns `"already-on-chain"`, on Liqwid it may return a protocol error even though the first attempt landed. Refetch orders instead.

---

## 9. TypeScript reference types

```ts
export type Source = "liqwid" | "surf";
export interface Asset { policyId: string; assetName: string; symbol: string; decimals: number; priceUsd?: number }
export interface SourcesMap { [source: string]: string }       // "" = ok, else error

export interface MarketsResponse { fetchedAt: string; sources: SourcesMap; markets: Market[] }
export interface OrdersResponse  { fetchedAt: string; sources: SourcesMap; orders: Order[] }

export interface TxParams {
  source: Source; marketId: string; amount: number; collateralAmount?: number; collateralUnit?: string; full?: boolean;
  wallet: string; address: string; changeAddress: string; otherAddresses: string[]; utxos: string[];
}
export interface TxCloseParams {
  source: Source; marketId: string; address: string; wallet: string; txHash: string; outputIndex: number;
  kind: "repay" | "cancel"; redeemCollateral: boolean; changeAddress: string; otherAddresses: string[]; utxos: string[]; pkh: string;
}
export interface BuiltTx { source: string; action: string; cbor: string; hint?: string; trackedOrder?: unknown; witnesses?: string[] }
export interface SubmitParams { source: Source; cbor: string; witnessSet: string; address: string; wallet: string }
export interface SubmitResponse { txHash: string }

export type Direction = "long" | "short";
export type JobStatus = "awaiting_funding" | "running" | "open" | "reversing" | "reversed" | "failed";
export interface OpenRequest { owner: string; direction: Direction; collateralUnit: string; targetUnit: string; amount: number; leverage: number; utxos?: string[]; changeAddress?: string }
export interface OpenResult  { jobId: string; tempAddress: string; fundingCbor: string }
export interface Leg { index: number; tradeInAda: number; supplyAmt: number; borrowAmt: number; cumExposure: number }
export interface Plan { direction: Direction; initialAmount: number; targetLeverage: number; achievedLeverage: number; legLtv: number; legs: Leg[]; totalExposure: number; totalBorrowed: number }
export interface Quote { plan: Plan; borrowSource: string; borrowMarket: string; borrowApy: number; legLtv: number; loops: number; txCount: number; liqMovePct: number; liqThreshold: number; feeReserveAda: number; totalAdaNeeded: number; minSupply?: number; note?: string }
export interface Step { kind: string; source?: string; marketId?: string; txHash?: string; url?: string; amount?: number; note?: string; at: string }
export interface Job {
  id: string; owner: string; direction: Direction; collateralUnit: string; targetUnit: string; initialAmount: number; leverage: number;
  plan: Plan; tempAddress: string; fundingLovelace: number; feeReserveLovelace: number; status: JobStatus; steps: Step[];
  exposure: number; healthFactor: number; entryPrice?: number; currentPrice?: number; pnlPct?: number; liqThreshold?: number;
  healthUpdatedAt?: string; error?: string; createdAt: string; updatedAt: string;
}

export interface StrikeStatus { builderEnabled: boolean; feeBps: number; connected: boolean }
export interface StrikeChallenge { nonce: string; messageToSign: string }
export interface StrikeVerify { connected: boolean; accountId: string }
export interface StrikeHistoryEntry { id: string; address: string; type: "deposit" | "withdraw"; asset: string; amount: string; usdValue: string; txHash: string; withdrawId: string; requestId: string; status: "confirmed" | "pending" | "sent" | "failed"; createdAt: string }
```
