#!/bin/bash

# Setup script for SSH Executor test environment
# This script helps prepare SSH and WinRM servers for testing

set -e

echo "========================================="
echo "SSH Executor Test Environment Setup"
echo "========================================="

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Function to print colored messages
print_info() {
    echo -e "${BLUE}[INFO]${NC} $1"
}

print_success() {
    echo -e "${GREEN}[SUCCESS]${NC} $1"
}

print_warning() {
    echo -e "${YELLOW}[WARNING]${NC} $1"
}

print_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

# Check if running as root (needed for some setup steps)
check_root() {
    if [[ $EUID -eq 0 ]]; then
        return 0
    else
        return 1
    fi
}

# Setup SSH server for testing
setup_ssh_server() {
    echo -e "\n${YELLOW}Setting up SSH Test Server${NC}"
    echo "========================================="
    
    # Get server details
    read -p "Enter SSH server IP/hostname [localhost]: " SSH_HOST
    SSH_HOST=${SSH_HOST:-localhost}
    
    read -p "Enter SSH port [22]: " SSH_PORT
    SSH_PORT=${SSH_PORT:-22}
    
    read -p "Enter test username [testuser]: " SSH_USER
    SSH_USER=${SSH_USER:-testuser}
    
    read -sp "Enter test user password: " SSH_PASS
    echo
    
    # Test SSH connection
    print_info "Testing SSH connection..."
    if ssh -o ConnectTimeout=5 -o StrictHostKeyChecking=no \
        ${SSH_USER}@${SSH_HOST} -p ${SSH_PORT} "echo 'SSH connection successful'" 2>/dev/null; then
        print_success "SSH connection test passed"
    else
        print_error "SSH connection test failed"
        print_info "Please ensure:"
        echo "  1. SSH server is running on ${SSH_HOST}:${SSH_PORT}"
        echo "  2. User ${SSH_USER} exists and can login"
        echo "  3. Password authentication is enabled"
        return 1
    fi
    
    # Create test directories on SSH server
    print_info "Creating test directories on SSH server..."
    ssh ${SSH_USER}@${SSH_HOST} -p ${SSH_PORT} << EOF
        mkdir -p /tmp/shexec_test
        mkdir -p ~/shexec_test
        echo "Test file" > /tmp/shexec_test/test.txt
        echo "SSH server setup complete" > ~/shexec_test/setup.log
EOF
    
    print_success "SSH test server configured"
    
    # Generate SSH key for key-based auth testing (optional)
    read -p "Generate SSH key for key-based authentication testing? (y/n): " GEN_KEY
    if [[ "$GEN_KEY" == "y" || "$GEN_KEY" == "Y" ]]; then
        KEY_PATH="./test_data/test_key"
        mkdir -p ./test_data
        
        print_info "Generating SSH key pair..."
        ssh-keygen -t rsa -b 2048 -f ${KEY_PATH} -N "" -q
        
        print_info "Copying public key to server..."
        ssh-copy-id -i ${KEY_PATH}.pub -p ${SSH_PORT} ${SSH_USER}@${SSH_HOST}
        
        # Convert to PEM format
        ssh-keygen -p -m PEM -f ${KEY_PATH} -N "" -q
        mv ${KEY_PATH} ${KEY_PATH}.pem
        
        print_success "SSH key generated at ${KEY_PATH}.pem"
    fi
    
    # Update test_env.json with SSH server details
    cat > test_env_ssh.tmp << EOL
    "ssh": {
        "host": "${SSH_HOST}",
        "port": ${SSH_PORT},
        "username": "${SSH_USER}",
        "password": "${SSH_PASS}",
        "pem_file_path": "./test_data/test_key.pem"
    }
EOL
    
    print_success "SSH server setup complete"
}

# Setup WinRM server for testing
setup_winrm_server() {
    echo -e "\n${YELLOW}Setting up WinRM Test Server${NC}"
    echo "========================================="
    
    print_info "For Windows Server setup, run these commands in PowerShell as Administrator:"
    echo
    cat << 'EOF'
# Enable WinRM
Enable-PSRemoting -Force

# Configure WinRM for HTTPS
$cert = New-SelfSignedCertificate -DnsName "localhost" -CertStoreLocation "cert:\LocalMachine\My"
New-Item -Path WSMan:\LocalHost\Listener -Transport HTTPS -Address * -CertificateThumbPrint $cert.Thumbprint -Force

# Configure WinRM settings
Set-Item WSMan:\localhost\Service\Auth\Basic -Value $true
Set-Item WSMan:\localhost\Service\AllowUnencrypted -Value $false
Set-Item WSMan:\localhost\MaxEnvelopeSizekb -Value 2048

# Configure firewall
New-NetFirewallRule -DisplayName "WinRM HTTPS" -Direction Inbound -Protocol TCP -LocalPort 5986 -Action Allow

# Restart WinRM service
Restart-Service WinRM

# Test WinRM
Test-WSMan -ComputerName localhost -UseSSL

# Create test directory
New-Item -ItemType Directory -Force -Path "C:\Temp\shexec_test"
EOF
    
    echo
    read -p "Have you run the above commands on your Windows server? (y/n): " WINRM_READY
    
    if [[ "$WINRM_READY" != "y" && "$WINRM_READY" != "Y" ]]; then
        print_warning "Please run the commands on your Windows server first"
        return 1
    fi
    
    # Get WinRM server details
    read -p "Enter WinRM server IP/hostname: " WINRM_HOST
    read -p "Enter WinRM port [5986]: " WINRM_PORT
    WINRM_PORT=${WINRM_PORT:-5986}
    
    read -p "Enter Windows username [Administrator]: " WINRM_USER
    WINRM_USER=${WINRM_USER:-Administrator}
    
    read -sp "Enter Windows password: " WINRM_PASS
    echo
    
    # Update test_env.json with WinRM server details
    cat > test_env_winrm.tmp << EOL
    "winrm": {
        "host": "${WINRM_HOST}",
        "port": ${WINRM_PORT},
        "username": "${WINRM_USER}",
        "password": "${WINRM_PASS}"
    }
EOL
    
    print_success "WinRM server configuration saved"
}

# Create test_env.json
create_test_env_json() {
    echo -e "\n${YELLOW}Creating test_env.json${NC}"
    echo "========================================="
    
    cat > test_env.json << 'EOF'
{
    "_comment": "Test environment configuration for SSH Executor",
    "test_servers": {
EOF
    
    # Add SSH config if exists
    if [ -f test_env_ssh.tmp ]; then
        cat test_env_ssh.tmp >> test_env.json
        
        # Add comma if WinRM config also exists
        if [ -f test_env_winrm.tmp ]; then
            echo "," >> test_env.json
        fi
    fi
    
    # Add WinRM config if exists
    if [ -f test_env_winrm.tmp ]; then
        cat test_env_winrm.tmp >> test_env.json
    fi
    
    cat >> test_env.json << 'EOF'
    }
}
EOF
    
    # Clean up temp files
    rm -f test_env_ssh.tmp test_env_winrm.tmp
    
    print_success "test_env.json created successfully"
}

# Verify test configuration
verify_test_config() {
    echo -e "\n${YELLOW}Verifying Test Configuration${NC}"
    echo "========================================="
    
    if [ ! -f test_env.json ]; then
        print_error "test_env.json not found"
        return 1
    fi
    
    if [ ! -f test_config.json ]; then
        print_error "test_config.json not found"
        return 1
    fi
    
    print_info "Running configuration validation..."
    if go run . --validate --config test_config.json 2>&1 | grep -q "Configuration is valid"; then
        print_success "Test configuration is valid"
    else
        print_warning "Configuration validation had issues - review the output"
    fi
    
    print_info "Test environment is ready!"
    echo
    echo "To run tests:"
    echo "  ./run_tests.sh           # Run basic tests"
    echo "  ./run_tests.sh --all     # Run all tests including integration"
    echo "  go test -v -run TestName # Run specific test"
}

# Main menu
main_menu() {
    echo
    echo "Select what you want to setup:"
    echo "1. SSH Test Server"
    echo "2. WinRM Test Server"
    echo "3. Both SSH and WinRM"
    echo "4. Verify existing configuration"
    echo "5. Exit"
    echo
    read -p "Enter your choice [1-5]: " CHOICE
    
    case $CHOICE in
        1)
            setup_ssh_server
            create_test_env_json
            verify_test_config
            ;;
        2)
            setup_winrm_server
            create_test_env_json
            verify_test_config
            ;;
        3)
            setup_ssh_server
            setup_winrm_server
            create_test_env_json
            verify_test_config
            ;;
        4)
            verify_test_config
            ;;
        5)
            echo "Exiting..."
            exit 0
            ;;
        *)
            print_error "Invalid choice"
            exit 1
            ;;
    esac
}

# Check prerequisites
check_prerequisites() {
    print_info "Checking prerequisites..."
    
    # Check for Go
    if ! command -v go &> /dev/null; then
        print_error "Go is not installed"
        exit 1
    fi
    
    # Check for SSH client
    if ! command -v ssh &> /dev/null; then
        print_error "SSH client is not installed"
        exit 1
    fi
    
    print_success "All prerequisites met"
}

# Main execution
check_prerequisites
main_menu

echo
print_success "Setup complete!"