# CommandGuard test environment setup

## Overview

The SSH Executor testing framework uses **real SSH and WinRM servers** for testing, ensuring that tests accurately reflect production behavior. This guide will help you set up dedicated test servers for comprehensive testing.

## Quick Start

1. **Reserve test servers**: Dedicate one SSH server and one WinRM server for testing
2. **Run setup script**: `./setup_test_env.sh`
3. **Run tests**: `./run_tests.sh`

## Test Server Requirements

### SSH Test Server
- **OS**: Linux/Unix (Ubuntu, CentOS, macOS, etc.)
- **Access**: SSH enabled on port 22 (or custom port)
- **User**: Dedicated test user with:
  - Password authentication enabled
  - Sudo access (optional, for privileged command tests)
  - Home directory write permissions
- **Test directories**: `/tmp/shexec_test` and `~/shexec_test`

### WinRM Test Server
- **OS**: Windows Server 2016+ or Windows 10/11 Pro
- **Access**: WinRM enabled on port 5986 (HTTPS)
- **User**: Administrator or user with administrative privileges
- **Configuration**: Basic authentication enabled, HTTPS listener configured
- **Test directory**: `C:\Temp\shexec_test`

## Setup Instructions

### Method 1: Automated Setup (Recommended)

Run the interactive setup script:

```bash
./setup_test_env.sh
```

The script will:
1. Guide you through SSH server configuration
2. Help configure WinRM server (provides PowerShell commands)
3. Test connections to both servers
4. Generate `test_env.json` with your server details
5. Optionally create SSH keys for key-based authentication testing

### Method 2: Manual Setup

#### Step 1: Configure SSH Test Server

On your SSH test server:

```bash
# Create test user (if not exists)
sudo adduser testuser
sudo usermod -aG sudo testuser  # Optional: for sudo tests

# Create test directories
sudo -u testuser mkdir -p /home/testuser/shexec_test
sudo mkdir -p /tmp/shexec_test
sudo chmod 777 /tmp/shexec_test

# Ensure password authentication is enabled
sudo sed -i 's/PasswordAuthentication no/PasswordAuthentication yes/' /etc/ssh/sshd_config
sudo systemctl restart sshd
```

#### Step 2: Configure WinRM Test Server

On your Windows test server, run in PowerShell as Administrator:

```powershell
# Enable WinRM
Enable-PSRemoting -Force

# Configure HTTPS listener
$cert = New-SelfSignedCertificate -DnsName "localhost" -CertStoreLocation "cert:\LocalMachine\My"
New-Item -Path WSMan:\LocalHost\Listener -Transport HTTPS -Address * -CertificateThumbPrint $cert.Thumbprint -Force

# Configure authentication
Set-Item WSMan:\localhost\Service\Auth\Basic -Value $true
Set-Item WSMan:\localhost\Service\AllowUnencrypted -Value $false

# Configure firewall
New-NetFirewallRule -DisplayName "WinRM HTTPS" -Direction Inbound -Protocol TCP -LocalPort 5986 -Action Allow

# Create test directory
New-Item -ItemType Directory -Force -Path "C:\Temp\shexec_test"

# Restart service
Restart-Service WinRM

# Verify setup
Test-WSMan -ComputerName localhost -UseSSL
```

#### Step 3: Create test_env.json

Copy `test_env.example.json` to `test_env.json` and update with your server details:

```json
{
    "test_servers": {
        "ssh": {
            "host": "192.168.1.100",
            "port": 22,
            "username": "testuser",
            "password": "YourTestPassword123"
        },
        "winrm": {
            "host": "192.168.1.101",
            "port": 5986,
            "username": "Administrator",
            "password": "YourWindowsPassword123!"
        }
    }
}
```

## Test Configuration Files

### test_config.json
Base configuration template with predefined test commands and reports. This file is committed to the repository.

### test_env.json
Your local environment configuration with actual server credentials. This file is **gitignored** and should never be committed.

### test_config_real.json
Generated automatically when tests run, combining `test_config.json` with server details from `test_env.json`.

## Running Tests

### Basic Testing

```bash
# Run all tests
go test

# Run with verbose output
go test -v

# Run specific test
go test -run TestCommandExecution
```

### Using Test Runner Script

```bash
# Run basic tests
./run_tests.sh

# Run with coverage
./run_tests.sh --coverage

# Run all tests including integration
./run_tests.sh --all

# Run with verbose output
./run_tests.sh --verbose
```

### Test Categories

1. **Unit Tests**: API endpoint validation, authentication flow
2. **Integration Tests**: End-to-end workflows, command chaining
3. **Performance Tests**: Large output handling, concurrent execution
4. **Security Tests**: Authentication, authorization, token validation

## Test Commands

The test configuration includes various commands for different test scenarios:

### SSH Commands
- `test-echo`: Simple echo command
- `test-date`: Get current date
- `test-pwd`: Print working directory
- `test-ls`: List directory contents
- `test-large-output`: Generate large output for truncation testing
- `test-error-cmd`: Command that returns error code
- `test-chain-first/second`: Command chaining tests

### WinRM Commands
- `test-win-echo`: Windows echo command
- `test-win-dir`: List Windows directory

### Reports
- `test-report`: Multi-command report
- `test-mixed-report`: Report with both SSH and WinRM commands

## Troubleshooting

### SSH Connection Issues

```bash
# Test SSH connection manually
ssh testuser@192.168.1.100 -p 22 "echo 'Connection successful'"

# Check SSH service
sudo systemctl status sshd

# Check firewall
sudo ufw status
```

### WinRM Connection Issues

```powershell
# Test WinRM locally
Test-WSMan -ComputerName localhost -UseSSL

# Check WinRM service
Get-Service WinRM

# Check listeners
Get-WSManInstance -ResourceURI winrm/config/listener -Enumerate

# Check firewall rules
Get-NetFirewallRule | Where DisplayName -like "*WinRM*"
```

### Common Issues

1. **"No test configuration found"**
   - Create `test_env.json` from `test_env.example.json`
   - Or ensure `test_config.json` exists with your server details

2. **"SSH connection refused"**
   - Verify SSH service is running
   - Check firewall rules
   - Ensure password authentication is enabled

3. **"WinRM connection failed"**
   - Verify WinRM service is running
   - Check HTTPS listener is configured
   - Ensure Basic authentication is enabled
   - Check Windows Firewall rules

4. **"Command not found" errors**
   - Ensure test commands exist on target servers
   - Check PATH environment variables
   - Verify user permissions

## Security Considerations

1. **Dedicated Test Servers**: Use isolated servers specifically for testing
2. **Network Isolation**: Consider using a separate network/VLAN for test servers
3. **Credentials**: Never use production credentials for testing
4. **Cleanup**: Regularly clean test artifacts from servers
5. **Access Control**: Limit test server access to development team

## Clean Up

After testing, clean up test artifacts:

```bash
# Remove test artifacts locally
rm -rf test_temp/
rm -f test_config_real.json
rm -f coverage.out coverage.html

# Clean up SSH server
ssh testuser@your-ssh-server "rm -rf ~/shexec_test /tmp/shexec_test"

# Clean up Windows server (PowerShell)
Remove-Item -Recurse -Force C:\Temp\shexec_test
```

## CI/CD Integration

For CI/CD pipelines, set up dedicated test servers and configure environment variables:

```yaml
# GitHub Actions example
env:
  TEST_SSH_HOST: ${{ secrets.TEST_SSH_HOST }}
  TEST_SSH_USER: ${{ secrets.TEST_SSH_USER }}
  TEST_SSH_PASS: ${{ secrets.TEST_SSH_PASS }}
  TEST_WINRM_HOST: ${{ secrets.TEST_WINRM_HOST }}
  TEST_WINRM_USER: ${{ secrets.TEST_WINRM_USER }}
  TEST_WINRM_PASS: ${{ secrets.TEST_WINRM_PASS }}
```

Then create `test_env.json` from environment variables in your CI script.

## Support

If you encounter issues:
1. Check server logs (`/var/log/auth.log` for SSH, Event Viewer for Windows)
2. Run tests with verbose flag: `go test -v`
3. Verify network connectivity between test client and servers
4. Ensure all prerequisites are installed (Go, SSH client)

## Next Steps

After setting up your test environment:
1. Run the full test suite to verify setup
2. Add custom test commands for your use cases
3. Integrate tests into your CI/CD pipeline
4. Regularly update test servers with security patches