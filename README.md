# Adding Leverage to DexHunter

## Introduction

### Project Overview

DH-Leverage is a comprehensive leveraged trading system designed to bring advanced DeFi capabilities to the Cardano ecosystem through DexHunter. This project bridges the gap between traditional spot trading and sophisticated leveraged positions by integrating multiple lending protocols with decentralized exchange infrastructure.

### Key Features

- **Multi-Protocol Integration**: Seamlessly connects with leading Cardano lending protocols (Liqwid, Levvy, Flow)
- **Automated Leverage Management**: Smart routing engine that optimizes borrowing rates across protocols
- **Real-Time Position Tracking**: Monitor and manage leveraged positions with live blockchain data
- **Risk Management**: Built-in safeguards for liquidation prevention and position monitoring
- **Unified API**: Single interface for accessing multiple lending sources and trading venues

### Problem Statement

The Cardano DeFi ecosystem lacks a unified platform for leveraged trading that:
- Aggregates liquidity from multiple lending protocols
- Provides seamless integration with existing DEX infrastructure
- Offers professional-grade trading tools for advanced users
- Maintains decentralization while improving user experience

### Solution Approach

DH-Leverage addresses these challenges by:
1. **Protocol Abstraction**: Creating a unified interface for different lending protocols
2. **Smart Order Routing**: Automatically selecting the best lending rates and liquidity sources
3. **Real-Time Data Processing**: Using Gouroboros for efficient blockchain data ingestion
4. **Modular Architecture**: Allowing easy integration of new protocols and features

### Initial Implementation

The project will initially focus on **Snek Token** as the core implementation token due to:
- Broadest liquidity depth on Cardano DEXs
- High trading volume and market activity
- Strong community support and adoption
- Established price discovery mechanisms

The architecture is designed for easy expansion to support additional tokens including:
- Stablecoins (DJED, iUSD, USDA)
- Blue-chip Cardano native tokens

### Target Users

- **Professional Traders**: Seeking advanced trading strategies on Cardano
- **DeFi Power Users**: Looking to maximize capital efficiency
- **Arbitrageurs**: Exploiting price differences across protocols
- **Liquidity Providers**: Earning yield through lending positions
- **Institutional Users**: Requiring programmatic access to leveraged positions


## Infrastructure

### Core Components

#### Blockchain Infrastructure
- **Cardano Node**: Full node with Unix socket connection for real-time blockchain data parsing
  - Syncs with Cardano mainnet for transaction monitoring
  - Provides access to UTxO state and smart contract interactions
  - Can be run locally or accessed via remote TCP connection
  - Alternative: Use existing node infrastructure via TCP/IP connection

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

#### Blockchain Data Ingestion
We leverage [Gouroboros](https://github.com/blinklabs-io/gouroboros) for efficient blockchain data processing:
- **Chain Sync Protocol**: Real-time synchronization with Cardano network
- **Block Processing**: Parses and filters relevant transactions
- **UTxO Tracking**: Monitors lending protocol smart contracts
- **Event Streaming**: Pushes relevant events to processing pipeline

#### Processing Architecture
- **Worker Processes**: Dedicated workers for different lending protocols
- **Event Queue**: Redis-based queue for asynchronous processing
- **API Gateway**: RESTful API for client interactions
- **WebSocket Server**: Real-time updates for active positions

### Deployment Modes

#### Full Node Deployment
Complete infrastructure with local Cardano node:
- Self-contained and independent
- No external dependencies for blockchain data
- Higher resource requirements
- Suitable for production environments

#### Light Deployment
Using external node infrastructure:
- Lower resource requirements
- Depends on external node reliability
- Faster initial setup
- Suitable for development and testing

#### Standalone Mode (Future)
The system is designed to eventually support standalone operation:
- Direct TCP connections to remote Cardano nodes
- Minimal infrastructure requirements
- User-friendly deployment
- Suitable for individual traders

## Available Leverage Sources
- [Liqwid](https://liqwid.finance/)
- [Levvy](https://levvy.fi/)
- [Flow](https://beta.flowcardano.org/)


## Lending Protocol Flows


## Overall Layout

### System Architecture

The DH-Leverage system follows a modular, event-driven architecture designed for scalability and maintainability. The following sections detail the various flows and components that enable leveraged trading on Cardano.

### Order Flow Documentation

Detailed protocol-specific implementations can be found in the following documents:
- [Levvy Protocol Integration](common/sources/levvy/levvy.md) - Flash loan based leverage
- [Liqwid Protocol Integration](common/sources/liqwid/liqwid.md) - Collateralized lending positions
- [Flow Protocol Integration](common/sources/flow/flow.md) - Peer-to-peer lending markets

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

## Requirements

### System Requirements

#### Production Environment with Cardano Node
- **CPU**: 8 cores (minimum) / 16 cores (recommended)
- **RAM**: 32 GB (minimum) / 64 GB (recommended)
- **Storage**: 600 GB SSD (minimum) / 1 TB NVMe SSD (recommended)
- **Network**: Stable internet connection with at least 100 Mbps bandwidth
- **OS**: Ubuntu 22.04 LTS, Debian 11, or RHEL 8+ (64-bit)

#### Production Environment without Cardano Node
- **CPU**: 4 cores (minimum) / 8 cores (recommended)
- **RAM**: 8 GB (minimum) / 16 GB (recommended)
- **Storage**: 150 GB SSD (minimum) / 250 GB SSD (recommended)
- **Network**: Stable internet connection with at least 50 Mbps bandwidth
- **OS**: Ubuntu 22.04 LTS, Debian 11, or RHEL 8+ (64-bit)

#### Development Environment
- **CPU**: 2 cores minimum
- **RAM**: 4 GB minimum
- **Storage**: 50 GB available space
- **OS**: Linux, macOS, or Windows with WSL2

### Software Dependencies

#### Required Software
- **Docker**: Version 20.10.0 or higher
- **Docker Compose**: Version 2.0.0 or higher
- **Go**: Version 1.21 or higher (for development)
- **Git**: Version 2.25 or higher

#### Database Requirements
- **MongoDB**: Version 7.0 (handled by Docker)
- **Redis**: Version 7.2 (handled by Docker)

#### Network Ports
The following ports must be available:
- `27017`: MongoDB
- `6379`: Redis
- `3001`: Cardano Node (if using)
- `8080` (or configured PORT): API Server

### API Keys and Credentials
- **DexHunter API Key**: Required for trading functionality
- **Cardano Node Access**: Either local node or remote node endpoint
- **MongoDB Credentials**: Configure in `.env` file
- **Redis Password**: Configure in `.env` file


### How to run

#### Quick Start

The easiest way to get started is using the interactive quickstart script:

```bash
./quickstart.sh
```

This script provides a user-friendly menu with the following options:

**Infrastructure Setup:**
- **Start with Cardano Node**: Launches MongoDB, Redis, and a Cardano node using Docker Compose
- **Start without Cardano Node**: Launches only MongoDB and Redis (for development or when using external node)

**Development Mode:**
- **Start API**: Runs the API server locally with `go run` for development
- **Start Worker**: Runs the worker process locally for block processing and data gathering
- **Start Both**: Simultaneously runs both API and Worker in development mode

**Production Deployment:**
- **Deploy API**: Builds and deploys the API in a Docker container with automatic restart
- **Deploy Worker**: Builds and deploys the Worker in a Docker container with automatic restart  
- **Deploy Both**: Deploys both services in production-ready Docker containers

**Monitoring:**
- **View Logs**: Interactive log viewer for all services (MongoDB, Redis, Cardano Node, API, Worker)

#### Prerequisites

1. **Copy and configure the environment file:**
   ```bash
   cp sample.env .env
   # Edit .env with your configuration
   ```

2. **Ensure required tools are installed:**
   - Docker and Docker Compose
   - Go 1.21+ (for development mode)
   - Git

#### Manual Setup

If you prefer manual setup over the quickstart script:

1. **Start infrastructure:**
   ```bash
   # With Cardano node
   docker-compose -f docker-compose-with-node.yml up -d
   
   # Without Cardano node
   docker-compose -f docker-compose-no-node.yml up -d
   ```

2. **Run the application:**
   ```bash
   # Run API
   go run main.go api
   
   # Run Worker
   go run main.go worker
   ```


## Deliverables
The complete project will be publicly available in this repository.

### Disclaimer
*The information provided in this document is subject to change during development as requirements evolve or initial approaches require modification.*