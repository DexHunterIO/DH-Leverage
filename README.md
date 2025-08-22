# Adding Leverage to DexHunter
Approved by Nikita Melentjevs - CEO & Edoardo Salvioni - Blockchain Lead on 18.08.2025

## Introduction
### Project Overview
DH-Leverage is a comprehensive leveraged trading system designed to bring advanced DeFi capabilities to the Cardano ecosystem through DexHunter. This project bridges the gap between traditional spot trading and sophisticated leveraged positions by integrating multiple lending protocols with decentralized exchange infrastructure.

[Catalyst Proposal](https://milestones.projectcatalyst.io/projects/1200019)

## Additional Resources

**Wireframe:** [Flow Diagram](https://github.com/DexHunterIO/DH-Leverage/blob/main/images/flow_diagram.png)

**Architecture:** [Technical Architecture Documentation](https://github.com/DexHunterIO/DH-Leverage/blob/main/architecture.md)

### Key Features

- **Multi-Protocol Integration**: Seamlessly connects with leading Cardano lending protocols (Liqwid, Levvy, Flow)
- **Automated Leverage Management**: Smart routing engine that optimizes borrowing rates across protocols
- **Real-Time Position Tracking**: Monitor and manage leveraged positions with live blockchain data
- **Risk Management**: Built-in safeguards for liquidation prevention and position monitoring
- **Unified API**: Single interface for accessing multiple lending sources and trading venues

### Problem Statement

The Cardano DeFi ecosystem currently lacks a unified platform for leveraged trading that aggregates liquidity from multiple lending protocols, allows for direct trades with existing dexes and improves the user experience.

### Solution Approach

These challenges are addressed by creating a unified interface to integrate different lending protocols, an engine that handles selection and routing to the various protocol for best rates and liquidity and allows easy integration of new protocols and features as they are required by the echosystem.

### Initial Implementation

The project will initially focus on **Snek Token** as the core implementation token due to:
- Broadest liquidity depth on Cardano DEXs
- High trading volume and market activity
- Strong community support and adoption
- Established price discovery mechanisms

The choice to support only SNEK at the beginning is due to the wide adoption of the token and its presence across all protocols within the cardano Ecosystem.

The architecture will be designed for easy expansion to support additional tokens including stablecoins Like USDA, USDM,DJED and other Bluechips

### Target Users

- **Professional Traders**: Seeking advanced trading strategies on Cardano
- **DeFi Power Users**: Looking to maximize capital efficiency
- **Arbitrageurs**: Exploiting price differences across protocols
- **Liquidity Providers**: Earning yield through lending positions
- **Institutional Users**: Requiring programmatic access to leveraged positions

## [Infrastructure & Architecture](architecture.md)


## Requirements

The requirements here provided are a mere estimation as of the current project, they might change as the development progresses.

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

Following you will find a quick guide on how to run the protocol for your own usage or to deploy and use within your app.

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

### Project Timelines

| Phase | Date | Status |
|-------|------|--------|
| **Milestone 1** | August 2025 | In Progress |
| **Milestone 2** | September 2025 | Upcoming |
| **Milestone 3** | November 2025 | Planned |
| **Milestone 4** | January 2026 | Planned |
| **Milestone 5** | February 2026 | Planned |
| **Final Milestone** | March 2026 | Planned |

---

### Project Catalyst Proposal

**Full Details:** [View our complete proposal and milestones on Project Catalyst](https://milestones.projectcatalyst.io/projects/1200019/milestones/1)

---

### Disclaimer

> *The information provided in this document is subject to change during development as requirements evolve or initial approaches require modification.*
