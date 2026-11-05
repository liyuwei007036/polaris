import paramiko

bwh = paramiko.SSHClient()
bwh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
bwh.connect('45.62.114.40', port=22, username='root', password='umFhEnEP2jvZ', timeout=10)

def run(cmd):
    stdin, stdout, stderr = bwh.exec_command(cmd)
    return stdout.read().decode().strip()

print("1. All running systemd services:")
print(run("systemctl list-units --type=service --state=running"))

print("\n2. All installed services with 'probe' or 'nezha' or 'monitor' or 'node' or 'exporter':")
print(run("systemctl list-unit-files | grep -E 'probe|nezha|monitor|node|exporter|stats|telemetry'"))

print("\n3. Processes in ps aux:")
print(run("ps aux"))

print("\n4. /etc/systemd/system/ contents:")
print(run("ls -la /etc/systemd/system/"))

print("\n5. /opt or /etc files:")
print(run("ls -la /opt; ls -la /etc/nezha* 2>/dev/null; ls -la /etc/polaris* 2>/dev/null"))

bwh.close()
