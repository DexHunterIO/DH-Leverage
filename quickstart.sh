#!/bin/bash

# DH-Leverage quickstart — interactive helper for the local dev/prod stack.
# DH-Leverage talks to each lending protocol over its public HTTP API, so
# no Cardano node is needed. The local infra is just MongoDB + Redis.

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

# Pin the compose project name so the network it creates is always
# ${COMPOSE_PROJECT}_app-network regardless of the checkout directory name.
COMPOSE_PROJECT="dh-leverage"
COMPOSE="docker compose -p ${COMPOSE_PROJECT} -f docker-compose-no-node.yml"
APP_NETWORK="${COMPOSE_PROJECT}_app-network"

print_color() {
    color=$1
    message=$2
    echo -e "${color}${message}${NC}"
}

check_env() {
    if [ ! -f .env ]; then
        print_color "$RED" "Error: .env file not found!"
        print_color "$YELLOW" "Run: cp sample.env .env && \$EDITOR .env"
        exit 1
    fi
}

load_env() {
    if [ -f .env ]; then
        export $(cat .env | grep -v '^#' | xargs)
    fi
}

show_menu() {
    echo
    print_color "$BLUE" "╔════════════════════════════════════════╗"
    print_color "$BLUE" "║     DH-Leverage Quick Start Menu      ║"
    print_color "$BLUE" "╚════════════════════════════════════════╝"
    echo
    print_color "$GREEN" "Infrastructure (MongoDB + Redis):"
    echo "  1) Start infra (docker compose up)"
    echo "  2) Stop  infra (docker compose down)"
    echo
    print_color "$GREEN" "Development:"
    echo "  3) Start API   (go run . api)"
    echo "  4) Start Worker (go run . worker)"
    echo "  5) Start Both"
    echo
    print_color "$GREEN" "Production deployment:"
    echo "  6) Deploy API as Docker container"
    echo
    print_color "$GREEN" "Monitoring:"
    echo "  7) View logs"
    echo
    print_color "$GREEN" "Other:"
    echo "  h) Help"
    echo "  x) Exit"
    echo
    echo -n "Enter your choice: "
}

# --- infra -----------------------------------------------------------------

start_infra() {
    print_color "$YELLOW" "Starting MongoDB + Redis…"
    $COMPOSE up -d
    print_color "$GREEN" "Infra started."
}

stop_infra() {
    print_color "$YELLOW" "Stopping MongoDB + Redis…"
    $COMPOSE down
    print_color "$GREEN" "Infra stopped."
}

# --- dev -------------------------------------------------------------------

start_api() {
    check_env
    load_env
    print_color "$YELLOW" "Starting API server… (http://localhost:${API_PORT:-8080})"
    go run . api
}

start_worker() {
    check_env
    load_env
    print_color "$YELLOW" "Starting Worker…"
    go run . worker
}

start_both() {
    check_env
    load_env
    print_color "$YELLOW" "Starting API and Worker…"
    go run . api &
    API_PID=$!
    go run . worker &
    WORKER_PID=$!
    print_color "$GREEN" "API (PID $API_PID) and Worker (PID $WORKER_PID) started."
    print_color "$YELLOW" "Press Ctrl+C to stop both."
    trap 'kill $API_PID $WORKER_PID 2>/dev/null' INT
    wait
}

# --- prod ------------------------------------------------------------------

deploy_api() {
    check_env
    load_env
    print_color "$YELLOW" "Building dh-leverage-api Docker image…"

    cat > Dockerfile.api <<'EOF'
FROM golang:1.24-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# -p 1 / GOMAXPROCS=1: compile one package at a time so the build fits in
# ~1 GB RAM (parallel compiles get OOM-killed on small hosts); -s -w strips
# debug info to shrink the binary.
RUN CGO_ENABLED=0 GOMAXPROCS=1 go build -p 1 -trimpath -ldflags="-s -w" -o /out/dh-leverage .

FROM alpine:latest
RUN apk --no-cache add ca-certificates
WORKDIR /app
COPY --from=builder /out/dh-leverage /app/dh-leverage
COPY .env /app/.env
EXPOSE 8080
CMD ["/app/dh-leverage", "api"]
EOF

    docker build -f Dockerfile.api -t dh-leverage-api .

    # The API container joins the compose network so DATABASE_URL / REDIS_URL
    # can use the service hostnames (mongodb / redis). Start the infra stack
    # if its network isn't there yet.
    if ! docker network inspect "$APP_NETWORK" >/dev/null 2>&1; then
        print_color "$YELLOW" "Network $APP_NETWORK not found — starting infra (MongoDB + Redis)…"
        $COMPOSE up -d || { print_color "$RED" "Failed to start infra."; return 1; }
    fi

    # Host port comes from API_PORT in .env (accepts "8080", ":8080" or
    # "0.0.0.0:8080"); inside the container the API always listens on 8080 so
    # the mapping can't drift from the listen port.
    HOST_PORT="${API_PORT##*:}"
    HOST_PORT="${HOST_PORT:-8080}"

    docker rm -f dh-leverage-api 2>/dev/null
    docker run -d \
        --name dh-leverage-api \
        --restart always \
        --env-file .env \
        -e API_PORT=8080 \
        -p "${HOST_PORT}:8080" \
        --network "$APP_NETWORK" \
        dh-leverage-api || { print_color "$RED" "Failed to start dh-leverage-api."; return 1; }

    print_color "$GREEN" "API deployed on http://localhost:${HOST_PORT}"
    print_color "$YELLOW" "Note: inside the container DATABASE_URL / REDIS_URL must use the service hostnames,"
    print_color "$YELLOW" "      e.g. mongodb://admin:<pw>@mongodb:27017/dh-leverage?authSource=admin and redis://:<pw>@redis:6379"
    print_color "$YELLOW" "      (localhost inside the container is the container itself). Check: docker logs dh-leverage-api"
}

# --- monitoring ------------------------------------------------------------

show_logs() {
    echo
    print_color "$BLUE" "Select logs to view:"
    echo "  1) MongoDB"
    echo "  2) Redis"
    echo "  3) API (Docker container)"
    echo "  4) docker-compose tail"
    echo "  b) Back"
    echo
    echo -n "Enter your choice: "
    read log_choice

    case $log_choice in
        1) docker logs -f mongodb ;;
        2) docker logs -f redis ;;
        3) docker logs -f dh-leverage-api ;;
        4) $COMPOSE logs -f ;;
        b) return ;;
        *) print_color "$RED" "Invalid option."; show_logs ;;
    esac
}

# --- help ------------------------------------------------------------------

show_help() {
    echo
    print_color "$BLUE" "╔════════════════════════════════════════╗"
    print_color "$BLUE" "║        DH-Leverage Help Guide         ║"
    print_color "$BLUE" "╚════════════════════════════════════════╝"
    echo
    print_color "$GREEN" "About:"
    echo "  DH-Leverage talks to lending protocols over their HTTP APIs."
    echo "  No Cardano node required — just MongoDB + Redis locally."
    echo
    print_color "$GREEN" "Infrastructure:"
    echo "  • Option 1: 'docker compose up' for MongoDB + Redis"
    echo "  • Option 2: 'docker compose down'"
    echo
    print_color "$GREEN" "Development:"
    echo "  • Option 3: Runs the API server with 'go run . api'"
    echo "  • Option 4: Runs the worker with 'go run . worker'"
    echo "  • Option 5: Runs both"
    echo
    print_color "$GREEN" "Production:"
    echo "  • Option 6: Builds and runs the API as a Docker container"
    echo
    print_color "$GREEN" "Monitoring:"
    echo "  • Option 7: Tail logs from any service"
    echo
    print_color "$YELLOW" "Prerequisites:"
    echo "  • Docker / docker compose"
    echo "  • Go 1.23+ (for the dev options)"
    echo "  • .env configured (cp sample.env .env)"
    echo
    echo "Press any key to return to the menu…"
    read -n 1
}

# --- main loop -------------------------------------------------------------

while true; do
    show_menu
    read choice

    case $choice in
        1) start_infra ;;
        2) stop_infra ;;
        3) start_api ;;
        4) start_worker ;;
        5) start_both ;;
        6) deploy_api ;;
        7) show_logs ;;
        h|H) show_help ;;
        x|X) print_color "$GREEN" "Goodbye!"; exit 0 ;;
        *) print_color "$RED" "Invalid option. Please try again." ;;
    esac
done
