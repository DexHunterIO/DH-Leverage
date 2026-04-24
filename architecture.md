
> **See also:** the running development log of decisions, integrations and known issues lives in [development-process.md](development-process.md).

## Infrastructure


### Core Components

#### Data Storage Layer
- **MongoDB (v7.0+)**: Primary database for persistent data storage
  - Stores historical leverage positions and orders
  - Maintains user transaction history
  - Indexes lending protocol states
  - Handles complex queries for analytics
  
- **Redis (v7.2+)**: High-performance caching and real-time data store
  - Caches frequently accessed lending rates
  - Stores active order book data
  - Manages session states and temporary data
  - Provides pub/sub for real-time updates

#### External Services
- **DexHunter API**: Trading execution and market data
  - Handles swap execution on supported DEXs
  - Provides real-time price feeds
  - Manages order routing and slippage protection
  - Required API key for authentication

### Data Pipeline Architecture

#### Protocol Data Ingestion
DH-Leverage talks to each lending protocol over its native HTTP API rather
than running its own Cardano node. Per-source notes:

- **Liqwid** — public GraphQL at `https://v2.api.liqwid.finance/graphql`
  for markets, loans and CBOR transaction builders.
- **Surf** — same-origin private REST routes under
  `https://surflending.org/api/*` (`getAllPoolInfos`, `getAllPositions`,
  `depositLiquidity`, `withdrawLiquidity`, `borrow`, …).
- **Wallet balances** — public Koios endpoints (`address_info`,
  `address_assets`).

#### Processing Architecture
- **API Gateway**: Fiber-based RESTful API the frontend talks to.
- **Per-source clients**: One Go package per protocol implementing a
  shared `Source` interface for markets/orders and `TxBuilder` interface
  for supply/withdraw/borrow.
- **Caching**: Redis-backed wrappers for markets, orders and wallet
  balances; in-memory fallback when Redis is unreachable.
- **Persistence**: MongoDB upsert of every fresh markets snapshot for
  historical analysis.

### Deployment Modes

#### Light Deployment (current)
The protocol APIs handle all on-chain interaction; DH-Leverage only needs
MongoDB + Redis locally. No Cardano node required.
- Lowest resource requirements
- Fastest setup
- Suitable for development and most production deployments

## Available Leverage Sources
- [Liqwid](https://liqwid.finance/) — pooled lending, native GraphQL integration
- [Surf](https://surflending.org/) — isolated-pool lending, native REST integration
- [Levvy](https://levvy.fi/) — P2P lending, no public mainnet API today (placeholder integration)


## Lending Protocol Flows


## Overall Layout

### System Architecture

The DH-Leverage system follows a modular, event-driven architecture designed for scalability and maintainability. The following sections detail the various flows and components that enable leveraged trading on Cardano.

### Order Flow Documentation

Detailed protocol-specific implementations can be found in the following documents:
- [Liqwid Protocol Integration](common/sources/liqwid/liqwid.md) - Collateralized lending positions
- [Surf Protocol Integration](common/sources/surf/flow.md) - Isolated-pool lending markets
- [Levvy Protocol Integration](common/sources/levvy/levvy.md) - P2P NFT/token lending

### Core Trading Flows

#### Abstract Leveraged Order Flow
![generic](images/flow_diagram.png)

The abstract flow demonstrates the universal process for executing leveraged trades regardless of the underlying lending protocol:
1. **Order Initiation**: User submits leverage parameters (amount, leverage ratio, direction)
2. **Protocol Selection**: System determines optimal lending source based on rates and availability
3. **Collateral Lock**: User's collateral is secured in smart contract
4. **Borrow Execution**: Funds are borrowed from selected protocol
5. **Trade Execution**: Combined funds are swapped via DexHunter
6. **Position Management**: System tracks position health and liquidation thresholds

#### Long Order Flow

**Objective**: Amplify exposure to price increases

**Process**:
1. **Deposit Collateral**: User deposits base token (e.g., ADA)
2. **Borrow Stablecoin**: System borrows ADA against collateral (Snek)
3. **Market Buy**: Execute buy order for target token with borrowed funds
4. **Position Tracking**: Monitor position value and health factor
5. **Close Position**: Sell tokens, repay loan, return profits/losses

**Example Scenario**:
- User deposits 1000 ADA and snek Collateral
- Borrows 1000 Ada at 2x leverage
- Buys Token tokens with combined 2000 ADA worth
- If SNEK increases 20%, user gains 40% on initial capital

#### Short Order Flow

**Objective**: Profit from price decreases

**Process**:
1. **Deposit Collateral**: User deposits ADA and Collateral
2. **Borrow Target Token**: System borrows target token against collateral
3. **Market Sell**: Immediately sell borrowed tokens for ADA
4. **Wait for Price Drop**: Monitor market for favorable conditions
5. **Buy Back and Repay**: Purchase tokens at lower price, repay loan, keep difference

**Example Scenario**:
- User deposits 1000 ADA as collateral
- Borrows 100 SNEK tokens
- Sells SNEK for ADA immediately
- If SNEK drops 20%, buy back for 800 ADA
- Profit: 200 ADA minus fees

#### Fulfill Long Flow

**Automated Execution for Long Positions**:

1. **Health Check**: Continuously monitor position health factor
2. **Liquidation Prevention**: 
   - Alert user when health factor < 1.5
   - Auto-deleverage option when health factor < 1.2
3. **Take Profit Execution**:
   - Trigger when target price is reached
   - Partial or full position closure
4. **Stop Loss Protection**:
   - Automatic closure at predetermined loss threshold
   - Slippage protection during volatile markets
5. **Settlement Process**:
   - Swap tokens back to repayment currency
   - Repay borrowed amount plus interest
   - Return remaining funds to user wallet

#### Fulfill Short Flow

**Automated Execution for Short Positions**:

1. **Borrow Rate Monitoring**: Track and optimize borrowing costs
2. **Margin Call Management**:
   - Monitor collateral ratio in real-time
   - Add collateral option before liquidation
3. **Profit Taking Strategy**:
   - Scale out of position at multiple price targets
   - Compound profits into new positions
4. **Risk Mitigation**:
   - Automatic buy-back if price rises beyond threshold
   - Emergency exit strategies during black swan events
5. **Final Settlement**:
   - Buy back borrowed tokens at market price
   - Return tokens to lending protocol
   - Calculate and distribute profits/losses


### Leverage Source Implementations
Leverage sources must handle data parsing from blocks to borrow or lend operations. They need to provide functions for the following operations:

- Place lend/borrow orders
- Cancel lend/borrow orders
- Fulfill lend/borrow orders
- Analyze blocks for new information

This will allow for the protocol to be expanded to include various other tokens and sources without too much hassle.


### Milestone Timeline
! Adjusted timelines due to health reasons.
Milestone 1: 21st August 2025
Milestone 2: 21st April 2026
Milestone 3: 21st may 2026
Milestone 4: 21st June 2026
Milestone 5: 21st July 2026
Final Milestone: July 2026