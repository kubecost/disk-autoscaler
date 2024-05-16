nocgo := "CGO_ENABLED=0"

default:
    just --list

check-das:
    git describe --tags --dirty --always

test-das:
    {{nocgo}} go test ./...

# user still has to configure KO_DOCKER_REPO for the destination of ko build image
# please refer https://ko.build/get-started/
ko-build VERSION:
    COMMIT_HASH=$(git rev-parse HEAD) VERSION={{VERSION}} \
    ko build \
    ./cmd/diskautoscaler \
    --bare -t {{VERSION}}
    echo "Built: {{VERSION}}"
