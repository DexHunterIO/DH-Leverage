# Adding Leverage to DexHunter

## Introduction

This document outlines the overall project plan for integrating leveraged swaps into DexHunter. The project will initially focus on using Snek Token for the core implementation, as it has the broadest liquidity on Cardano. However, the system can be easily expanded to support other token IDs.

## Infrastructure

To run this project, the following components are required:
- A Cardano node with Unix socket connection for data parsing
- A MongoDB server for data persistence
- A Redis server for quick data access and caching
- A DexHunter API key for the trading functionality

For data gathering, we will leverage [Gouroboros](https://github.com/blinklabs-io/gouroboros).

The program can eventually be run as a standalone application by users leveraging TCP connections to nodes.

## Available Leverage Sources
- [Liqwid](https://liqwid.finance/)
- [Levvy](https://levvy.fi/)
- [Flow](https://beta.flowcardano.org/)


## Lending Protocol Flows


## Overall Layout

The following diagrams illustrate how orders will work, including generic examples and specific long/short scenarios. You can find source-specific abstract flows that cover borrowing, lending, and canceling orders on lending sources:

- [Levvy](common/sources/levvy/levvy.md)
- [Liqwid](common/sources/liqwid/liqwid.md)
- [Flow](common/sources/flow/flow.md)

### Abstract Leveraged Order Flow
![generic](images/flow_diagram.png)

#### Long Order Flow

#### Short Order Flow

#### Fulfill Long Flow

#### Fulfill Short Flow

### Leverage Source Implementations
Leverage sources must handle data parsing from blocks to borrow or lend operations. They need to provide functions for the following operations:

- Place lend/borrow orders
- Cancel lend/borrow orders
- Fulfill lend/borrow orders
- Analyze blocks for new information


## TODOs
- [ ] Create initial Golang structs to handle data gathering
- [ ] Develop initial vendor interfaces for adding leverage sources
- [ ] Wire data gathering from node data feed
- [ ] Create various leverage source implementations
- [ ] Create engine that determines from which source the leverage should be taken
- [ ] Add stand-in burner wallet for handling lent deposits and subsequent trades
- [ ] Handle trades using DexHunter API


## Deliverables
The complete project will be publicly available in this repository.

### Disclaimer
*The information provided in this document is subject to change during development as requirements evolve or initial approaches require modification.*