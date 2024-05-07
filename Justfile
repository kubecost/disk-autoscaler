nocgo := "CGO_ENABLED=0"

default:
    just --list

check-das:
    git describe --tags --dirty --always

test-das:
    {{nocgo}} go test ./...

dev-build TAG: check-das test-das
    docker buildx build \
    --rm \
    --platform "linux/amd64" \
    -f ./Dockerfile \
    --provenance=false \
    --load \
    -t {{TAG}} \
    .

    echo "Built: {{TAG}}"
