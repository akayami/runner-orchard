[CmdletBinding()]
param(
    [ValidateSet('Configure','Enroll','Activate','Status','Pause','Resume','Uninstall')]
    [string]$Action = 'Enroll',
    [uri]$ConsoleUrl,
    [ValidatePattern('^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$')]
    [string]$Name = "$env:COMPUTERNAME-orchard",
    [string]$OrchardExe,
    [string]$WorkerTokenFile,
    [string]$VaultProject,
    [string]$RunnerBackupSecret,
    [switch]$InstallTray,
    [switch]$InstallTools,
    [switch]$ManageACPower,
    [double]$Hours = 4
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$root = Join-Path $env:ProgramData 'RunnerOrchard'
$runner = Join-Path $root 'runner'
$private = Join-Path $root 'private'
$binary = Join-Path $root 'orchard.exe'
$registrationPath = Join-Path $private 'registration.json'
$configurationPath = Join-Path $private 'supervisor.json'
$runnerService = 'actions.runner.Orchard.Windows'
$supervisorService = 'RunnerOrchardSupervisor'
$backupMarker = Join-Path $private 'backup-complete.json'

if ($Action -in 'Status','Pause','Resume') {
    if ($Action -eq 'Pause') {
        & $binary pause -hours ($Hours.ToString([Globalization.CultureInfo]::InvariantCulture))
    } else {
        & $binary ($Action.ToLowerInvariant())
    }
    exit $LASTEXITCODE
}

if (!$ConsoleUrl -or !$ConsoleUrl.IsAbsoluteUri -or $ConsoleUrl.Scheme -ne 'https' -or
    $ConsoleUrl.UserInfo -or $ConsoleUrl.Query -or $ConsoleUrl.Fragment -or $ConsoleUrl.AbsolutePath -ne '/') {
    throw 'Provide the trusted HTTPS console origin, with no path or credentials.'
}
$origin = $ConsoleUrl.GetLeftPart([UriPartial]::Authority)
if ($Action -eq 'Configure') {
    @{console_url=$origin; name=$Name; root=$root; runner_service=$runnerService; supervisor_service=$supervisorService} |
        ConvertTo-Json
    return
}
$identity = [Security.Principal.WindowsIdentity]::GetCurrent()
$principal = [Security.Principal.WindowsPrincipal]::new($identity)
if (!$principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
    throw 'Run enrollment and activation from an elevated PowerShell session.'
}
if ([Runtime.InteropServices.RuntimeInformation]::OSArchitecture -ne 'X64') {
    throw 'Orchard currently supports Windows x64 only.'
}

Add-Type -AssemblyName System.Net.Http
$handler = [Net.Http.HttpClientHandler]::new()
$handler.AllowAutoRedirect = $false
$http = [Net.Http.HttpClient]::new($handler)
$http.Timeout = [TimeSpan]::FromSeconds(30)
$http.MaxResponseContentBufferSize = 65536

function Invoke-OrchardJson($path, $body, $token) {
    $request = [Net.Http.HttpRequestMessage]::new([Net.Http.HttpMethod]::Post, "$origin$path")
    try {
        $request.Content = [Net.Http.StringContent]::new(($body | ConvertTo-Json -Compress),
            [Text.Encoding]::UTF8, 'application/json')
        $request.Content.Headers.ContentType.CharSet = $null
        if ($token) {
            $request.Headers.Authorization = [Net.Http.Headers.AuthenticationHeaderValue]::new('Bearer', $token)
        }
        try { $reply = $http.SendAsync($request).GetAwaiter().GetResult() }
        catch { throw 'Orchard HTTPS connection failed. Check certificate trust and connectivity.' }
        try {
            if (!$reply.IsSuccessStatusCode) { throw "Orchard rejected the request (HTTP $([int]$reply.StatusCode))." }
            return $reply.Content.ReadAsStringAsync().GetAwaiter().GetResult() | ConvertFrom-Json
        } finally { $reply.Dispose() }
    } finally { $request.Dispose() }
}

function Assert-RegularPath($path) {
    if ((Get-Item -LiteralPath $path -Force).Attributes -band [IO.FileAttributes]::ReparsePoint) {
        throw "Orchard path is a reparse point: $path"
    }
}

function Initialize-Directory {
    if (!(Test-Path -LiteralPath $root)) {
        $acl = [Security.AccessControl.DirectorySecurity]::new()
        $acl.SetOwner([Security.Principal.SecurityIdentifier]::new('S-1-5-32-544'))
        $acl.SetAccessRuleProtection($true, $false)
        foreach ($sid in 'S-1-5-18','S-1-5-32-544') {
            $rule = [Security.AccessControl.FileSystemAccessRule]::new(
                [Security.Principal.SecurityIdentifier]::new($sid), 'FullControl',
                'ContainerInherit,ObjectInherit', 'None', 'Allow')
            $acl.AddAccessRule($rule)
        }
        [IO.Directory]::CreateDirectory($root, $acl) | Out-Null
    }
    Assert-RegularPath $root
    $actual = Get-Acl -LiteralPath $root
    $owner = $actual.GetOwner([Security.Principal.SecurityIdentifier]).Value
    if (!$actual.AreAccessRulesProtected -or $owner -notin 'S-1-5-18','S-1-5-32-544') {
        throw 'Existing Orchard root must be owned by Administrators or SYSTEM with protected permissions.'
    }
    $writes = [Security.AccessControl.FileSystemRights]::WriteData -bor [Security.AccessControl.FileSystemRights]::AppendData -bor
        [Security.AccessControl.FileSystemRights]::DeleteSubdirectoriesAndFiles -bor [Security.AccessControl.FileSystemRights]::ChangePermissions -bor
        [Security.AccessControl.FileSystemRights]::TakeOwnership -bor [Security.AccessControl.FileSystemRights]::Delete
    foreach ($rule in $actual.GetAccessRules($true, $true, [Security.Principal.SecurityIdentifier])) {
        if ($rule.AccessControlType -eq 'Allow' -and ($rule.FileSystemRights -band $writes) -and
            $rule.IdentityReference.Value -notin 'S-1-5-18','S-1-5-32-544') {
            throw 'Existing Orchard root grants write access outside Administrators and SYSTEM.'
        }
    }
    if (!(Test-Path -LiteralPath $private)) { New-Item -ItemType Directory $private | Out-Null }
    Assert-RegularPath $private
}

function Grant-Folder($path, $account, $rights) {
    Assert-RegularPath $path
    $acl = Get-Acl -LiteralPath $path
    $rule = [Security.AccessControl.FileSystemAccessRule]::new($account, $rights,
        'ContainerInherit,ObjectInherit', 'None', 'Allow')
    $acl.SetAccessRule($rule)
    Set-Acl -LiteralPath $path -AclObject $acl
}

function Write-Json($path, $value) {
    [IO.File]::WriteAllText($path, ($value | ConvertTo-Json -Compress), [Text.UTF8Encoding]::new($false))
}

function Register-Runner {
    $bytes = [byte[]]::new(32)
    $random = [Security.Cryptography.RandomNumberGenerator]::Create()
    try { $random.GetBytes($bytes) } finally { $random.Dispose() }
    $secret = [BitConverter]::ToString($bytes).Replace('-', '').ToLowerInvariant()
    $enrollment = Invoke-OrchardJson '/api/enrollment' @{name=$Name;os='win';architecture='x64';secret=$secret} $null
    if ($enrollment.id -notmatch '^[a-f0-9]{24}$') { throw 'Invalid Orchard enrollment identifier.' }
    Write-Host "Approve code $($enrollment.id) using the Windows profile in $origin."
    $deadline = [DateTime]::UtcNow.AddMinutes(10)
    do {
        $claim = Invoke-OrchardJson "/api/enrollment/$($enrollment.id)/claim" @{} $secret
        if ($claim.status -eq 'ready') { break }
        if ($claim.status -ne 'pending') { throw 'Windows enrollment did not complete.' }
        Start-Sleep -Seconds 3
    } while ([DateTime]::UtcNow -lt $deadline)
    if ($claim.status -ne 'ready') { throw 'Windows enrollment expired.' }
    $package = $claim.package
    $approved = $claim.runner
    $url = [uri]$package.download_url
    if ($package.os -ne 'win' -or $package.architecture -ne 'x64' -or
        $url.Scheme -ne 'https' -or $url.Host -ne 'github.com' -or $url.UserInfo -or
        $url.AbsolutePath -notmatch '^/actions/runner/releases/download/v[0-9.]+/actions-runner-win-x64-[0-9.]+\.zip$' -or
        $package.sha256_checksum -notmatch '^[a-fA-F0-9]{64}$' -or $approved.name -notmatch '^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$' -or
        $approved.organization -notmatch '^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$') {
        throw 'Orchard returned invalid Windows package or registration metadata.'
    }
    $archive = Join-Path $private 'runner.zip'
    try {
        Invoke-WebRequest -UseBasicParsing -Uri $url -OutFile $archive -TimeoutSec 180
        if ((Get-FileHash -LiteralPath $archive -Algorithm SHA256).Hash -ne $package.sha256_checksum) {
            throw 'GitHub runner archive checksum mismatch.'
        }
        Expand-Archive -LiteralPath $archive -DestinationPath $runner
    } finally {
        if (Test-Path -LiteralPath $archive) { Remove-Item -LiteralPath $archive }
    }
    Write-Json $registrationPath @{origin=$origin; requested_name=$Name; name=$approved.name; organization=$approved.organization}
    $listener = Join-Path $runner 'bin/Runner.Listener.exe'
    try {
        $env:ACTIONS_RUNNER_INPUT_TOKEN = $claim.registration_token
        Push-Location -LiteralPath $runner
        try {
            & $listener configure --unattended --url "https://github.com/$($approved.organization)" --name $approved.name --runnergroup $approved.runner_group --labels orchard-managed --work _work *> $null
            if ($LASTEXITCODE -ne 0) { throw 'GitHub configuration failed. Review the partial installation before retrying.' }
        } finally { Pop-Location }
    } finally {
        Remove-Item Env:ACTIONS_RUNNER_INPUT_TOKEN -ErrorAction SilentlyContinue
        $claim = $null
    }
}

function New-OrchardService($serviceName, $command, $startMode) {
    $existing = Get-CimInstance -ClassName Win32_Service -Filter "Name='$serviceName'"
    if ($existing) {
        if ($existing.PathName -ne $command -or $existing.StartName -ne "NT SERVICE\$serviceName" -or
            $existing.StartMode -ne $(if ($startMode -eq 'Automatic') { 'Auto' } else { 'Manual' })) {
            throw "Existing service $serviceName differs from the Orchard configuration."
        }
        return
    }
    $created = Invoke-CimMethod -ClassName Win32_Service -MethodName Create -Arguments @{
        Name=$serviceName; DisplayName=$serviceName; PathName=$command; ServiceType=[byte]16;
        ErrorControl=[byte]1; StartMode=$startMode; DesktopInteract=$false;
        StartName="NT SERVICE\$serviceName"; StartPassword=''
    }
    if ($created.ReturnValue -ne 0) { throw "Could not create $serviceName (Windows status $($created.ReturnValue))." }
}

function Grant-RunnerControl {
    $sid = [Security.Principal.NTAccount]::new("NT SERVICE\$supervisorService").Translate([Security.Principal.SecurityIdentifier])
    $current = & sc.exe sdshow $runnerService | Where-Object { $_ -match '^D:' } | Select-Object -First 1
    if ($LASTEXITCODE -ne 0 -or !$current) { throw 'Cannot read runner service permissions.' }
    $descriptor = [Security.AccessControl.RawSecurityDescriptor]::new($current.Trim())
    $mask = 0x35
    foreach ($entry in $descriptor.DiscretionaryAcl) {
        if ($entry.SecurityIdentifier -eq $sid -and ($entry.AccessMask -band $mask) -eq $mask) { return }
    }
    $ace = [Security.AccessControl.CommonAce]::new('None', 'AccessAllowed', $mask, $sid, $false, $null)
    $descriptor.DiscretionaryAcl.InsertAce($descriptor.DiscretionaryAcl.Count, $ace)
    & sc.exe sdset $runnerService ($descriptor.GetSddlForm('All')) | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'Cannot grant supervisor access to the runner service.' }
}

function Install-Prerequisites {
    if (!(Get-Command winget.exe -ErrorAction SilentlyContinue)) { throw 'WinGet is required for automatic tooling installation.' }
    function Install-Tool($id, $extra) {
        & winget.exe install --id $id --exact --source winget --scope machine --disable-interactivity --accept-package-agreements --accept-source-agreements @extra
        if ($LASTEXITCODE -in 3010,1641) { throw "Windows restart required after installing $id. Rerun setup after reboot." }
        if ($LASTEXITCODE -ne 0) { throw "WinGet installation failed for $id (status $LASTEXITCODE)." }
    }
    $git = Join-Path $env:ProgramFiles 'Git/cmd/git.exe'
    if (!(Test-Path -LiteralPath $git)) { Install-Tool 'Git.Git' @() }
    & $git lfs version
    if ($LASTEXITCODE -ne 0) { throw 'Install Git LFS in the machine-wide Git installation.' }
    $pwsh = Join-Path $env:ProgramFiles 'PowerShell/7/pwsh.exe'
    if (!(Test-Path -LiteralPath $pwsh)) { Install-Tool 'Microsoft.PowerShell' @() }
    $python = Join-Path $env:ProgramFiles 'Python312/python.exe'
    if (!(Test-Path -LiteralPath $python)) { Install-Tool 'Python.Python.3.12' @() }
    $vswhere = Join-Path ${env:ProgramFiles(x86)} 'Microsoft Visual Studio/Installer/vswhere.exe'
    $visualStudio = $null
    if (Test-Path -LiteralPath $vswhere) {
        $visualStudio = & $vswhere -latest -products '*' -requires Microsoft.VisualStudio.Component.VC.Tools.x86.x64 -property installationPath
    }
    if (!$visualStudio) {
        $override = '--quiet --wait --norestart --add Microsoft.VisualStudio.Workload.VCTools --includeRecommended'
        Install-Tool 'Microsoft.VisualStudio.2022.BuildTools' @('--override',$override)
    }
    $sdk = Join-Path ${env:ProgramFiles(x86)} 'Windows Kits/10/bin'
    if (!(Get-ChildItem -LiteralPath $sdk -Filter rc.exe -Recurse -ErrorAction SilentlyContinue |
        Where-Object { $_.Directory.Name -eq 'x64' } | Select-Object -First 1)) {
        throw 'A Windows SDK with x64 resource tools is required. Check the Visual Studio installer and pending restarts.'
    }
}

try {
    $installed = Get-Service -Name $supervisorService -ErrorAction SilentlyContinue
    if ($Action -eq 'Uninstall') {
        Assert-RegularPath $root
        Initialize-Directory
        $configuration = Get-Content -LiteralPath $configurationPath -Raw | ConvertFrom-Json
        if (!$configuration.lifecycle_endpoint.StartsWith("$origin/api/windows-workers/") -or $configuration.runner_service -ne $runnerService) {
            throw 'Existing service configuration does not match this console.'
        }
        if ($installed -and $installed.Status -ne 'Stopped') {
            # The supervisor holds StopPending while the assigned job drains.
            $service = Get-CimInstance -ClassName Win32_Service -Filter "Name='$supervisorService'"
            $stop = Invoke-CimMethod -InputObject $service -MethodName StopService
            if ($stop.ReturnValue -notin 0,5) { throw "Cannot request supervisor stop (Windows status $($stop.ReturnValue))." }
            $installed.WaitForStatus('Stopped', [TimeSpan]::FromMinutes(30))
        }
        $native = Get-Service -Name $runnerService -ErrorAction SilentlyContinue
        if ($native -and $native.Status -ne 'Stopped') { throw 'The GitHub listener is still running.' }
        $powerSnapshot = Join-Path $private 'power-plan.json'
        if (Test-Path -LiteralPath $powerSnapshot) {
            & $binary power-restore -file $powerSnapshot
            if ($LASTEXITCODE -ne 0) { throw 'Could not restore the saved AC power settings.' }
        }
        $removalMarker = Join-Path $private 'uninstalled.json'
        if (!(Test-Path -LiteralPath $removalMarker)) {
            $token = [IO.File]::ReadAllText((Join-Path $private 'worker-token')).Trim()
            $path = ([uri]$configuration.lifecycle_endpoint).AbsolutePath
            $deadline = [DateTime]::UtcNow.AddMinutes(1)
            do {
                try { $removed = Invoke-OrchardJson $path @{remove=$true} $token; break }
                catch {
                    if ([DateTime]::UtcNow -ge $deadline) { throw 'GitHub did not confirm runner removal. Services remain stopped.' }
                    Start-Sleep -Seconds 5
                }
            } while ($true)
            $token = $null
            if (!$removed.removed) { throw 'GitHub did not confirm runner removal.' }
            Write-Json $removalMarker @{removed=[DateTime]::UtcNow.ToString('o')}
        }
        foreach ($nameToRemove in $supervisorService,$runnerService) {
            if (Get-Service -Name $nameToRemove -ErrorAction SilentlyContinue) {
                & sc.exe delete $nameToRemove | Out-Null
                if ($LASTEXITCODE -ne 0) { throw "Could not delete $nameToRemove." }
            }
        }
        Remove-ItemProperty -LiteralPath 'HKLM:\SOFTWARE\Microsoft\Windows\CurrentVersion\Run' -Name RunnerOrchard -ErrorAction SilentlyContinue
        Write-Host 'Services and GitHub registration removed. The private directory remains for explicit cleanup and audit.'
        return
    }
    if ($installed -and $installed.Status -eq 'Running') {
        $existing = Get-Content -LiteralPath $registrationPath -Raw | ConvertFrom-Json
        if ($Action -ne 'Activate' -or $existing.origin -ne $origin -or $existing.requested_name -ne $Name) {
            throw 'Orchard is already active. Use Status or explicit migration.'
        }
        & $binary status
        exit $LASTEXITCODE
    }
    if ((Get-Service -Name 'actions.runner.*' -ErrorAction SilentlyContinue | Where-Object Name -NE $runnerService) -or
        (Get-Process -Name 'Runner.Listener' -ErrorAction SilentlyContinue)) {
        throw 'An existing runner is present. Drain and migrate it explicitly before continuing.'
    }
    Initialize-Directory
    if (Test-Path -LiteralPath (Join-Path $private 'uninstalled.json')) { throw 'A previous registration was removed. Review its private files before reinstalling.' }
    if ($InstallTools) { Install-Prerequisites }
    $runnerMetadata = Join-Path $runner '.runner'
    if ($Action -eq 'Enroll') {
        if (!$VaultProject -or $RunnerBackupSecret -notmatch '^runner-orchard-windows-[a-z0-9-]+$') {
            throw 'Provide the GCP project and pre-created machine-specific runner backup secret.'
        }
        $vault = & gcloud secrets describe $RunnerBackupSecret --project $VaultProject --format 'value(name)'
        if ($LASTEXITCODE -ne 0 -or !$vault) { throw 'Cannot verify the GCP runner identity backup target.' }
        if (!(Test-Path -LiteralPath $runnerMetadata)) {
            if (Test-Path -LiteralPath $runner) { throw 'Existing runner directory requires explicit recovery.' }
            Register-Runner
        }
    }
    if (!(Test-Path -LiteralPath $runnerMetadata) -or !(Test-Path -LiteralPath $registrationPath)) {
        throw 'Complete enrollment before activation.'
    }
    Assert-RegularPath $runner
    $registration = Get-Content -LiteralPath $registrationPath -Raw | ConvertFrom-Json
    if ($registration.origin -ne $origin -or $registration.requested_name -ne $Name) {
        throw 'Enrollment exists with different settings. Use explicit migration.'
    }
    $metadata = Get-Content -LiteralPath $runnerMetadata -Raw | ConvertFrom-Json
    if ($metadata.agentId -le 0 -or $metadata.agentName -ne $registration.name) { throw 'Runner metadata does not match enrollment.' }
    if ($Action -eq 'Enroll') {
        if (Test-Path -LiteralPath $backupMarker) {
            $previous = Get-Content -LiteralPath $backupMarker -Raw | ConvertFrom-Json
            if ($previous.project -ne $VaultProject -or $previous.secret -ne $RunnerBackupSecret) {
                throw 'This runner was backed up to a different vault reference.'
            }
        } else {
            $backup = @{}
            foreach ($file in '.runner','.credentials','.credentials_rsaparams') {
                $backup[$file] = [Convert]::ToBase64String([IO.File]::ReadAllBytes((Join-Path $runner $file)))
            }
            ($backup | ConvertTo-Json -Compress) | & gcloud secrets versions add $RunnerBackupSecret --project $VaultProject --data-file=- --quiet --format 'value(name)' | Out-Null
            if ($LASTEXITCODE -ne 0) { throw 'Runner identity backup failed. The runner remains offline.' }
            Write-Json $backupMarker @{project=$VaultProject; secret=$RunnerBackupSecret}
        }
        @{worker_id=$registration.name; organization=$registration.organization; runner_id=$metadata.agentId; status='registered-offline'} |
            ConvertTo-Json
        return
    }
    if (!(Test-Path -LiteralPath $backupMarker)) { throw 'Complete the runner vault backup before activation.' }
    if (!$WorkerTokenFile -or !$OrchardExe) { throw 'Provide the supervisor executable and vaulted lifecycle credential file.' }
    $token = [IO.File]::ReadAllText((Resolve-Path -LiteralPath $WorkerTokenFile)).Trim()
    if ($token -notmatch '^[a-fA-F0-9]{64}$') { throw 'Invalid lifecycle credential file.' }
    $endpoint = "/api/windows-workers/$($registration.name)"
    $remote = Invoke-OrchardJson $endpoint @{eligible=$false} $token
    if ($remote.runner_id -ne $metadata.agentId -or $remote.eligible -or $remote.busy -or $remote.online) {
        throw 'The lifecycle binding must match this runner, which must be offline and idle.'
    }
    $tokenPath = Join-Path $private 'worker-token'
    [IO.File]::WriteAllText($tokenPath, $token, [Text.UTF8Encoding]::new($false))
    $token = $null
    Copy-Item -LiteralPath $OrchardExe -Destination $binary
    $powerSnapshot = Join-Path $private 'power-plan.json'
    if ($ManageACPower -and !(Test-Path -LiteralPath $powerSnapshot)) {
        & powercfg.exe /a
        & $binary power-snapshot -file $powerSnapshot
        if ($LASTEXITCODE -ne 0) { throw 'Could not snapshot the current AC power settings.' }
    }
    if (!$ManageACPower -and (Test-Path -LiteralPath $powerSnapshot)) {
        & $binary power-restore -file $powerSnapshot
        if ($LASTEXITCODE -ne 0) { throw 'Could not restore the current AC power settings.' }
    }
    Write-Json $configurationPath @{
        runner_service=$runnerService; runner_dir=$runner; lifecycle_endpoint="$origin$endpoint";
        token_file=$tokenPath; pause_file=(Join-Path $private 'pause-until');
        power_plan_file=$(if ($ManageACPower) { $powerSnapshot } else { '' });
        collector=@{endpoint="$origin/api/telemetry"; name=$registration.name; id_file=(Join-Path $private 'collector.id'); runner_dirs=@($runner)}
    }
    $launcher = Join-Path $runner 'bin/RunnerService.exe'
    if (!(Get-Service -Name $runnerService -ErrorAction SilentlyContinue)) {
        # A service can update its own binaries after installation. Do not
        # run any of them elevated during repeat activation.
        $runnerAcl = Get-Acl -LiteralPath $runner
        if ($runnerAcl.Access | Where-Object { $_.IdentityReference -eq "NT SERVICE\$runnerService" }) {
            throw 'Review the existing runner files before recreating a removed service.'
        }
        & $launcher init *> $null
        if ($LASTEXITCODE -ne 0) { throw 'Could not initialize GitHub runner service logging.' }
    }
    New-OrchardService $runnerService ('"{0}"' -f $launcher) 'Manual'
    New-OrchardService $supervisorService ('"{0}" supervise -config "{1}"' -f $binary,$configurationPath) 'Automatic'
    $serviceMarker = Join-Path $runner '.service'
    if (!(Test-Path -LiteralPath $serviceMarker)) {
        [IO.File]::WriteAllText($serviceMarker, $runnerService, [Text.UTF8Encoding]::new($false))
    }
    Grant-Folder $runner "NT SERVICE\$runnerService" 'Modify'
    Grant-Folder $private "NT SERVICE\$supervisorService" 'Modify'
    $metadataAcl = Get-Acl -LiteralPath $runnerMetadata
    $metadataRead = [Security.AccessControl.FileSystemAccessRule]::new("NT SERVICE\$supervisorService", 'Read', 'Allow')
    $metadataAcl.SetAccessRule($metadataRead)
    Set-Acl -LiteralPath $runnerMetadata -AclObject $metadataAcl
    Grant-RunnerControl
    & sc.exe failure $supervisorService reset= 86400 actions= 'restart/3000/restart/5000/restart/10000' | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'Could not configure supervisor recovery.' }
    $rootAcl = Get-Acl -LiteralPath $root
    $execute = [Security.AccessControl.FileSystemAccessRule]::new(
        [Security.Principal.SecurityIdentifier]::new('S-1-5-32-545'), 'ReadAndExecute', 'None', 'None', 'Allow')
    $rootAcl.SetAccessRule($execute)
    Set-Acl -LiteralPath $root -AclObject $rootAcl
    $binaryAcl = Get-Acl -LiteralPath $binary
    $binaryAcl.SetAccessRule($execute)
    Set-Acl -LiteralPath $binary -AclObject $binaryAcl
    if ($InstallTray) {
        $tray = Join-Path $root 'tray.ps1'
        Copy-Item -LiteralPath (Join-Path $PSScriptRoot 'tray.ps1') -Destination $tray
        $trayAcl = Get-Acl -LiteralPath $tray
        $trayAcl.SetAccessRule($execute)
        Set-Acl -LiteralPath $tray -AclObject $trayAcl
        $startup = Join-Path $env:SystemRoot 'System32/WindowsPowerShell/v1.0/powershell.exe'
        $command = ('"{0}" -NoProfile -WindowStyle Hidden -Sta -File "{1}"' -f $startup,$tray)
        Set-ItemProperty -LiteralPath 'HKLM:\SOFTWARE\Microsoft\Windows\CurrentVersion\Run' -Name RunnerOrchard -Value $command
    }
    Start-Service -Name $supervisorService
    & $binary status
    exit $LASTEXITCODE
} finally {
    $http.Dispose()
}
