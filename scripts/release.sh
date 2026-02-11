#!/bin/bash

set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# Parse command line arguments
VERSION=""
PRERELEASE=false

usage() {
    echo "Usage: $0 <version> [--prerelease]"
    echo ""
    echo "Examples:"
    echo "  $0 1.0.0              # Create release v1.0.0"
    echo "  $0 1.2.3-beta.1       # Create pre-release v1.2.3-beta.1"
    echo "  $0 2.0.0 --prerelease # Create v2.0.0 as pre-release"
    echo ""
    echo "Version format: MAJOR.MINOR.PATCH[-PRERELEASE]"
    exit 1
}

# Parse arguments
if [ $# -lt 1 ]; then
    usage
fi

VERSION="$1"
shift

while [ $# -gt 0 ]; do
    case "$1" in
        --prerelease)
            PRERELEASE=true
            shift
            ;;
        *)
            echo -e "${RED}Error: Unknown option $1${NC}"
            usage
            ;;
    esac
done

# Validate version format
if ! [[ $VERSION =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[a-zA-Z0-9.]+)?$ ]]; then
    echo -e "${RED}Error: Invalid version format: $VERSION${NC}"
    echo "Expected format: MAJOR.MINOR.PATCH[-PRERELEASE]"
    echo "Examples: 1.0.0, 2.3.1, 1.0.0-beta.1, 2.0.0-rc.2"
    exit 1
fi

# Check if tag already exists
if git rev-parse "v$VERSION" >/dev/null 2>&1; then
    echo -e "${RED}Error: Tag v$VERSION already exists${NC}"
    exit 1
fi

# Check git status
if ! git diff-index --quiet HEAD --; then
    echo -e "${RED}Error: Working directory is not clean${NC}"
    echo "Please commit or stash your changes before releasing"
    git status --short
    exit 1
fi

# Ensure we're on main branch
CURRENT_BRANCH=$(git rev-parse --abbrev-ref HEAD)
if [ "$CURRENT_BRANCH" != "main" ]; then
    echo -e "${YELLOW}Warning: You're not on the main branch (current: $CURRENT_BRANCH)${NC}"
    read -p "Continue anyway? [y/N] " -n 1 -r
    echo
    if [[ ! $REPLY =~ ^[Yy]$ ]]; then
        exit 1
    fi
fi

# Pull latest changes
echo -e "${GREEN}Pulling latest changes...${NC}"
git pull --rebase

# Run build validation
echo ""
echo -e "${GREEN}Running build validation...${NC}"
echo "----------------------------------------"

# Check if go is installed
if ! command -v go &> /dev/null; then
    echo -e "${RED}Error: Go is not installed${NC}"
    exit 1
fi

# Download dependencies
echo "Downloading dependencies..."
if ! go mod download; then
    echo -e "${RED}Error: Failed to download Go modules${NC}"
    exit 1
fi

# Run build
echo "Building project..."
if ! go build -ldflags "-X github.com/moneat/agent/internal/collectors.AgentVersion=${VERSION}" ./...; then
    echo -e "${RED}Error: Build failed${NC}"
    exit 1
fi

# Run tests
echo "Running tests..."
if ! go test ./...; then
    echo -e "${RED}Error: Tests failed${NC}"
    exit 1
fi

echo -e "${GREEN}✓ Build validation passed${NC}"
echo ""

# Display what will be released
echo ""
echo "========================================="
echo "Release Summary"
echo "========================================="
echo "Version:     v$VERSION"
echo "Pre-release: $PRERELEASE"
echo "Branch:      $CURRENT_BRANCH"
echo ""

# Show commits since last tag
LAST_TAG=$(git describe --tags --abbrev=0 2>/dev/null || echo "")
if [ -n "$LAST_TAG" ]; then
    echo "Changes since $LAST_TAG:"
    git log $LAST_TAG..HEAD --oneline --no-merges | head -20
else
    echo "This is the first release"
    echo "Recent commits:"
    git log --oneline --no-merges | head -20
fi

echo ""
echo "========================================="
echo ""

# Confirm
read -p "Create and push tag v$VERSION? [y/N] " -n 1 -r
echo
if [[ ! $REPLY =~ ^[Yy]$ ]]; then
    echo "Release cancelled"
    exit 1
fi

# Create tag
echo -e "${GREEN}Creating tag v$VERSION...${NC}"
TAG_MESSAGE="Release v$VERSION"
if [ "$PRERELEASE" = true ] || [[ $VERSION == *"-"* ]]; then
    TAG_MESSAGE="Pre-release v$VERSION"
fi

git tag -a "v$VERSION" -m "$TAG_MESSAGE"

# Push tag
echo -e "${GREEN}Pushing tag to origin...${NC}"
git push origin "v$VERSION"

echo ""
echo -e "${GREEN}✓ Release v$VERSION created successfully!${NC}"
echo ""
echo "Next steps:"
echo "  1. GitHub Actions will automatically build and push Docker images"
echo "  2. Monitor the workflow at: https://github.com/moneat/agent/actions"
echo "  3. Docker images will be available at: https://hub.docker.com/r/moneat/agent"
echo ""

if [[ $VERSION != *"-"* ]] && [ "$PRERELEASE" != true ]; then
    echo "Docker tags created:"
    MAJOR=$(echo $VERSION | cut -d. -f1)
    MINOR=$(echo $VERSION | cut -d. -f1-2)
    echo "  - moneat/agent:$VERSION"
    echo "  - moneat/agent:$MINOR"
    echo "  - moneat/agent:$MAJOR"
    echo "  - moneat/agent:latest"
else
    echo "Docker tags created:"
    echo "  - moneat/agent:$VERSION"
fi

echo ""
