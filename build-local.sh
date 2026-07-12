#!/bin/bash
# Local build script for MMFP Govee - compiles CSS and builds Docker image

set -e

# Colors for output
GREEN='\033[0;32m'
BLUE='\033[0;34m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

echo -e "${BLUE}========================================${NC}"
echo -e "${BLUE}  Building MMFP Govee${NC}"
echo -e "${BLUE}========================================${NC}"

# Get version info
VERSION=$(cat VERSION 2>/dev/null || echo "dev")
BUILD_DATE=$(date -u +"%Y-%m-%dT%H:%M:%SZ")
COMMIT=$(git rev-parse --short HEAD 2>/dev/null || echo "unknown")

echo -e "\n${BLUE}Version:     ${GREEN}${VERSION}${NC}"
echo -e "${BLUE}Build Date:  ${GREEN}${BUILD_DATE}${NC}"
echo -e "${BLUE}Commit:      ${GREEN}${COMMIT}${NC}\n"

# Step 1: Install npm dependencies if needed
if [ ! -d "node_modules" ]; then
    echo -e "${YELLOW}Installing npm dependencies...${NC}"
    npm install
    echo -e "${GREEN}✓ Dependencies installed${NC}\n"
fi

# Step 2: Compile Tailwind CSS
echo -e "${YELLOW}Compiling Tailwind CSS...${NC}"
npm run build:css
echo -e "${GREEN}✓ CSS compiled${NC}\n"

# Step 3: Build Docker image
echo -e "${YELLOW}Building Docker image...${NC}"
docker build \
    --no-cache \
    --build-arg VERSION="$VERSION" \
    --build-arg BUILD_DATE="$BUILD_DATE" \
    --build-arg COMMIT="$COMMIT" \
    -t mmfp-govee:local \
    -t mmfp-govee:$VERSION \
    .
echo -e "${GREEN}✓ Docker image built${NC}\n"

echo -e "${GREEN}========================================${NC}"
echo -e "${GREEN}  Build Complete!${NC}"
echo -e "${GREEN}========================================${NC}"
echo -e "${BLUE}Images:${NC}"
echo -e "  - ${GREEN}mmfp-govee:local${NC}"
echo -e "  - ${GREEN}mmfp-govee:${VERSION}${NC}\n"
echo -e "${BLUE}Run:${NC} docker run -d -p 3008:3008 -p 8787:8787 -v \$(pwd)/config:/app/config mmfp-govee:local\n"
