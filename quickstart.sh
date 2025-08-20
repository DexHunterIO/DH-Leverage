#!/bin/bash

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Function to print colored output
print_color() {
    color=$1
    message=$2
    echo -e "${color}${message}${NC}"
}

# Function to check if .env file exists
check_env() {
    if [ ! -f .env ]; then
        print_color "$RED" "Error: .env file not found!"
        print_color "$YELLOW" "Please create .env file from sample.env"
        exit 1
    fi
}

# Function to load environment variables
load_env() {
    if [ -f .env ]; then
        export $(cat .env | grep -v '^#' | xargs)
    fi
}

# Function to display menu
show_menu() {
    echo
    print_color "$BLUE" "╔════════════════════════════════════════╗"
    print_color "$BLUE" "║     DH-Leverage Quick Start Menu      ║"
    print_color "$BLUE" "╚════════════════════════════════════════╝"
    echo
    print_color "$GREEN" "Infrastructure:"
    echo "  1) Start with Cardano Node"
    echo "  2) Start without Cardano Node"
    echo
    print_color "$GREEN" "Development:"
    echo "  3) Start API (go run)"
    echo "  4) Start Worker (go run)"
    echo "  5) Start Both (API + Worker)"
    echo
    print_color "$GREEN" "Production Deployment:"
    echo "  6) Deploy API (Docker)"
    echo "  7) Deploy Worker (Docker)"
    echo "  8) Deploy Both (Docker)"
    echo
    print_color "$GREEN" "Monitoring:"
    echo "  9) View Logs"
    echo
    print_color "$GREEN" "Other:"
    echo "  h) Help"
    echo "  x) Exit"
    echo
    echo -n "Enter your choice: "
}

# Function to start infrastructure with node
start_with_node() {
    print_color "$YELLOW" "Starting infrastructure with Cardano Node..."
    docker-compose -f docker-compose-with-node.yml up -d
    print_color "$GREEN" "Infrastructure with Cardano Node started successfully!"
}

# Function to start infrastructure without node
start_without_node() {
    print_color "$YELLOW" "Starting infrastructure without Cardano Node..."
    docker-compose -f docker-compose-no-node.yml up -d
    print_color "$GREEN" "Infrastructure without Cardano Node started successfully!"
}

# Function to start API
start_api() {
    check_env
    load_env
    print_color "$YELLOW" "Starting API server..."
    go run main.go api
}

# Function to start Worker
start_worker() {
    check_env
    load_env
    print_color "$YELLOW" "Starting Worker..."
    go run main.go worker
}

# Function to start both API and Worker
start_both() {
    check_env
    load_env
    print_color "$YELLOW" "Starting API and Worker..."
    go run main.go api &
    API_PID=$!
    go run main.go worker &
    WORKER_PID=$!
    print_color "$GREEN" "API (PID: $API_PID) and Worker (PID: $WORKER_PID) started!"
    print_color "$YELLOW" "Press Ctrl+C to stop both services"
    wait
}

# Function to deploy API in Docker
deploy_api() {
    check_env
    load_env
    print_color "$YELLOW" "Building and deploying API in Docker..."
    
    # Create Dockerfile for API if it doesn't exist
    cat > Dockerfile.api <<EOF
FROM golang:1.21-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -o main .

FROM alpine:latest
RUN apk --no-cache add ca-certificates
WORKDIR /root/
COPY --from=builder /app/main .
COPY .env .
EXPOSE ${PORT:-8080}
CMD ["./main", "api"]
EOF
    
    docker build -f Dockerfile.api -t dh-leverage-api .
    docker run -d \
        --name dh-leverage-api \
        --restart always \
        --env-file .env \
        -p ${PORT:-8080}:${PORT:-8080} \
        --network docker-compose-no-node_app-network \
        dh-leverage-api
    
    print_color "$GREEN" "API deployed successfully on port ${PORT:-8080}!"
}

# Function to deploy Worker in Docker
deploy_worker() {
    check_env
    load_env
    print_color "$YELLOW" "Building and deploying Worker in Docker..."
    
    # Create Dockerfile for Worker if it doesn't exist
    cat > Dockerfile.worker <<EOF
FROM golang:1.21-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -o main .

FROM alpine:latest
RUN apk --no-cache add ca-certificates
WORKDIR /root/
COPY --from=builder /app/main .
COPY .env .
CMD ["./main", "worker"]
EOF
    
    docker build -f Dockerfile.worker -t dh-leverage-worker .
    docker run -d \
        --name dh-leverage-worker \
        --restart always \
        --env-file .env \
        --network docker-compose-no-node_app-network \
        dh-leverage-worker
    
    print_color "$GREEN" "Worker deployed successfully!"
}

# Function to deploy both API and Worker
deploy_both() {
    deploy_api
    deploy_worker
    print_color "$GREEN" "Both API and Worker deployed successfully!"
}

# Function to show logs menu
show_logs() {
    echo
    print_color "$BLUE" "Select logs to view:"
    echo "  1) MongoDB logs"
    echo "  2) Redis logs"
    echo "  3) Cardano Node logs"
    echo "  4) API logs (Docker)"
    echo "  5) Worker logs (Docker)"
    echo "  6) All Docker Compose logs"
    echo "  b) Back to main menu"
    echo
    echo -n "Enter your choice: "
    read log_choice
    
    case $log_choice in
        1)
            print_color "$YELLOW" "Showing MongoDB logs (Ctrl+C to exit)..."
            docker logs -f mongodb
            ;;
        2)
            print_color "$YELLOW" "Showing Redis logs (Ctrl+C to exit)..."
            docker logs -f redis
            ;;
        3)
            print_color "$YELLOW" "Showing Cardano Node logs (Ctrl+C to exit)..."
            docker logs -f cardano-node
            ;;
        4)
            print_color "$YELLOW" "Showing API logs (Ctrl+C to exit)..."
            docker logs -f dh-leverage-api
            ;;
        5)
            print_color "$YELLOW" "Showing Worker logs (Ctrl+C to exit)..."
            docker logs -f dh-leverage-worker
            ;;
        6)
            print_color "$YELLOW" "Showing all Docker Compose logs (Ctrl+C to exit)..."
            docker-compose -f docker-compose-with-node.yml logs -f 2>/dev/null || \
            docker-compose -f docker-compose-no-node.yml logs -f
            ;;
        b)
            return
            ;;
        *)
            print_color "$RED" "Invalid option!"
            show_logs
            ;;
    esac
}

# Function to show help
show_help() {
    echo
    print_color "$BLUE" "╔════════════════════════════════════════╗"
    print_color "$BLUE" "║        DH-Leverage Help Guide         ║"
    print_color "$BLUE" "╚════════════════════════════════════════╝"
    echo
    print_color "$GREEN" "Infrastructure Options:"
    echo "  • Option 1: Starts MongoDB, Redis, and Cardano Node using docker-compose"
    echo "  • Option 2: Starts only MongoDB and Redis (no Cardano Node)"
    echo
    print_color "$GREEN" "Development Options:"
    echo "  • Option 3: Runs the API server locally using 'go run'"
    echo "  • Option 4: Runs the Worker locally using 'go run'"
    echo "  • Option 5: Runs both API and Worker locally"
    echo
    print_color "$GREEN" "Production Deployment:"
    echo "  • Option 6: Builds and deploys API in a Docker container with auto-restart"
    echo "  • Option 7: Builds and deploys Worker in a Docker container with auto-restart"
    echo "  • Option 8: Deploys both API and Worker in Docker containers"
    echo
    print_color "$GREEN" "Monitoring:"
    echo "  • Option 9: View logs from various services"
    echo
    print_color "$YELLOW" "Prerequisites:"
    echo "  • Docker and Docker Compose installed"
    echo "  • Go 1.21+ installed (for development options)"
    echo "  • .env file configured (copy from sample.env)"
    echo
    print_color "$YELLOW" "Notes:"
    echo "  • Production deployments use Docker with automatic restart"
    echo "  • API port is configured via PORT in .env file"
    echo "  • All services connect via the app-network Docker network"
    echo
    echo "Press any key to return to menu..."
    read -n 1
}

# Main loop
while true; do
    show_menu
    read choice
    
    case $choice in
        1)
            start_with_node
            ;;
        2)
            start_without_node
            ;;
        3)
            start_api
            ;;
        4)
            start_worker
            ;;
        5)
            start_both
            ;;
        6)
            deploy_api
            ;;
        7)
            deploy_worker
            ;;
        8)
            deploy_both
            ;;
        9)
            show_logs
            ;;
        h|H)
            show_help
            ;;
        x|X)
            print_color "$GREEN" "Goodbye!"
            exit 0
            ;;
        *)
            print_color "$RED" "Invalid option! Please try again."
            ;;
    esac
done