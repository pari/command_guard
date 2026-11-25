#!/bin/bash

# SSH Executor Test Runner Script
# This script runs all tests for the SSH Executor application

set -e

echo "========================================="
echo "SSH Executor Test Suite"
echo "========================================="

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# Check if Go is installed
if ! command -v go &> /dev/null; then
    echo -e "${RED}Error: Go is not installed${NC}"
    exit 1
fi

# Clean up any previous test artifacts
echo "Cleaning up test artifacts..."
rm -rf shexec_test_* 2>/dev/null || true
rm -f coverage.out coverage.html 2>/dev/null || true

# Run tests with different modes
run_test_suite() {
    local test_type=$1
    local test_flags=$2
    
    echo -e "\n${YELLOW}Running $test_type tests...${NC}"
    
    if go test $test_flags; then
        echo -e "${GREEN}✓ $test_type tests passed${NC}"
        return 0
    else
        echo -e "${RED}✗ $test_type tests failed${NC}"
        return 1
    fi
}

# Parse command line arguments
COVERAGE=false
VERBOSE=false
INTEGRATION=false
BENCHMARK=false

while [[ $# -gt 0 ]]; do
    case $1 in
        --coverage|-c)
            COVERAGE=true
            shift
            ;;
        --verbose|-v)
            VERBOSE=true
            shift
            ;;
        --integration|-i)
            INTEGRATION=true
            shift
            ;;
        --benchmark|-b)
            BENCHMARK=true
            shift
            ;;
        --all|-a)
            COVERAGE=true
            INTEGRATION=true
            BENCHMARK=true
            shift
            ;;
        --help|-h)
            echo "Usage: $0 [OPTIONS]"
            echo ""
            echo "Options:"
            echo "  -c, --coverage     Generate coverage report"
            echo "  -v, --verbose      Run tests in verbose mode"
            echo "  -i, --integration  Run integration tests"
            echo "  -b, --benchmark    Run benchmark tests"
            echo "  -a, --all          Run all tests with coverage"
            echo "  -h, --help         Show this help message"
            echo ""
            echo "Examples:"
            echo "  $0                 # Run basic tests"
            echo "  $0 --coverage      # Run tests with coverage"
            echo "  $0 --all           # Run all tests including integration"
            exit 0
            ;;
        *)
            echo -e "${RED}Unknown option: $1${NC}"
            echo "Use --help for usage information"
            exit 1
            ;;
    esac
done

# Build test flags
TEST_FLAGS=""
if [ "$VERBOSE" = true ]; then
    TEST_FLAGS="$TEST_FLAGS -v"
fi

# Track overall success
ALL_PASSED=true

# 1. Run unit tests
echo -e "\n${YELLOW}1. Unit Tests${NC}"
echo "----------------------------------------"
if [ "$COVERAGE" = true ]; then
    if ! run_test_suite "Unit" "$TEST_FLAGS -short -coverprofile=coverage.out -covermode=atomic"; then
        ALL_PASSED=false
    fi
else
    if ! run_test_suite "Unit" "$TEST_FLAGS -short"; then
        ALL_PASSED=false
    fi
fi

# 2. Run integration tests (if requested)
if [ "$INTEGRATION" = true ]; then
    echo -e "\n${YELLOW}2. Integration Tests${NC}"
    echo "----------------------------------------"
    if ! run_test_suite "Integration" "$TEST_FLAGS -run TestIntegration"; then
        ALL_PASSED=false
    fi
fi

# 3. Run benchmark tests (if requested)
if [ "$BENCHMARK" = true ]; then
    echo -e "\n${YELLOW}3. Benchmark Tests${NC}"
    echo "----------------------------------------"
    if ! go test -bench=. -benchmem -run=^$ $TEST_FLAGS; then
        ALL_PASSED=false
    fi
fi

# 4. Run race condition detection
echo -e "\n${YELLOW}4. Race Condition Detection${NC}"
echo "----------------------------------------"
if ! go test -race -short $TEST_FLAGS; then
    echo -e "${RED}✗ Race conditions detected${NC}"
    ALL_PASSED=false
else
    echo -e "${GREEN}✓ No race conditions detected${NC}"
fi

# 5. Generate coverage report (if requested)
if [ "$COVERAGE" = true ] && [ -f coverage.out ]; then
    echo -e "\n${YELLOW}5. Coverage Report${NC}"
    echo "----------------------------------------"
    
    # Generate HTML coverage report
    go tool cover -html=coverage.out -o coverage.html
    
    # Display coverage summary
    echo "Coverage Summary:"
    go tool cover -func=coverage.out | grep total | awk '{print "Total Coverage: " $3}'
    
    echo -e "${GREEN}Coverage report generated: coverage.html${NC}"
fi

# 6. Run go vet
echo -e "\n${YELLOW}6. Static Analysis (go vet)${NC}"
echo "----------------------------------------"
if go vet ./...; then
    echo -e "${GREEN}✓ No issues found${NC}"
else
    echo -e "${RED}✗ Issues found${NC}"
    ALL_PASSED=false
fi

# 7. Check for formatting issues
echo -e "\n${YELLOW}7. Code Formatting Check${NC}"
echo "----------------------------------------"
if [ -z "$(gofmt -l .)" ]; then
    echo -e "${GREEN}✓ Code is properly formatted${NC}"
else
    echo -e "${RED}✗ Code formatting issues found:${NC}"
    gofmt -l .
    ALL_PASSED=false
fi

# Final summary
echo ""
echo "========================================="
if [ "$ALL_PASSED" = true ]; then
    echo -e "${GREEN}✓ All tests passed successfully!${NC}"
    exit 0
else
    echo -e "${RED}✗ Some tests failed. Please review the output above.${NC}"
    exit 1
fi