$ErrorActionPreference = 'Stop'
Add-Type -AssemblyName System.Windows.Forms
Add-Type -AssemblyName System.Drawing

$mutex = [Threading.Mutex]::new($false, 'Local\RunnerOrchardTray')
if (!$mutex.WaitOne(0)) { $mutex.Dispose(); return }

function Invoke-Control($arguments) {
    $start = [Diagnostics.ProcessStartInfo]::new()
    $start.FileName = Join-Path $PSScriptRoot 'orchard.exe'
    $start.Arguments = $arguments
    $start.UseShellExecute = $false
    $start.CreateNoWindow = $true
    $start.RedirectStandardOutput = $true
    $start.RedirectStandardError = $true
    $process = [Diagnostics.Process]::Start($start)
    try {
        if (!$process.WaitForExit(4000)) { $process.Kill(); throw 'Orchard did not respond.' }
        if ($process.ExitCode -ne 0) { throw 'Orchard service is unavailable.' }
        return $process.StandardOutput.ReadToEnd() | ConvertFrom-Json
    } finally { $process.Dispose() }
}

function Show-Status($state) {
    $label = ($state.state -replace '-', ' ')
    if ($state.busy) { $label += ', job running' }
    if ($state.power_warning) { $label += ', check AC power settings' }
    if ($state.pause_until) {
        $remaining = ([DateTimeOffset]$state.pause_until - [DateTimeOffset]::UtcNow).TotalMinutes
        if ($remaining -gt 0) { $label += ", $([Math]::Ceiling($remaining)) min left" }
    }
    $status.Text = $label
    $tooltip = "Orchard: $label"
    $icon.Text = $tooltip.Substring(0, [Math]::Min($tooltip.Length, 63))
    $pause.Enabled = $true
    $resume.Enabled = [bool]$state.pause_until
}

function Refresh-Status {
    try { Show-Status (Invoke-Control 'status') }
    catch {
        $status.Text = 'Service unavailable'
        $icon.Text = 'Orchard: service unavailable'
        $pause.Enabled = $false
        $resume.Enabled = $false
    }
}

$menu = [Windows.Forms.ContextMenuStrip]::new()
$status = $menu.Items.Add('Checking Orchard status')
$status.Enabled = $false
$pause = $menu.Items.Add('Pause...')
$resume = $menu.Items.Add('Resume')
$exit = $menu.Items.Add('Exit tray app')
$icon = [Windows.Forms.NotifyIcon]::new()
$icon.Icon = [Drawing.SystemIcons]::Application
$icon.Text = 'Runner Orchard'
$icon.ContextMenuStrip = $menu
$icon.Visible = $true

$pause.Add_Click({
    $dialog = [Windows.Forms.Form]::new()
    $dialog.Text = 'Pause Runner Orchard'
    $dialog.FormBorderStyle = 'FixedDialog'
    $dialog.StartPosition = 'CenterScreen'
    $dialog.ClientSize = [Drawing.Size]::new(260, 100)
    $dialog.MaximizeBox = $false
    $dialog.MinimizeBox = $false
    $label = [Windows.Forms.Label]::new()
    $label.Text = 'Hours'
    $label.Location = [Drawing.Point]::new(14, 18)
    $label.AutoSize = $true
    $duration = [Windows.Forms.NumericUpDown]::new()
    $duration.Location = [Drawing.Point]::new(78, 15)
    $duration.DecimalPlaces = 2
    $duration.Minimum = 0.01
    $duration.Maximum = 8760
    $duration.Value = 4
    $ok = [Windows.Forms.Button]::new()
    $ok.Text = 'Pause'
    $ok.Location = [Drawing.Point]::new(78, 54)
    $ok.DialogResult = 'OK'
    $cancel = [Windows.Forms.Button]::new()
    $cancel.Text = 'Cancel'
    $cancel.Location = [Drawing.Point]::new(163, 54)
    $cancel.DialogResult = 'Cancel'
    $dialog.AcceptButton = $ok
    $dialog.CancelButton = $cancel
    $dialog.Controls.AddRange(@($label, $duration, $ok, $cancel))
    try {
        if ($dialog.ShowDialog() -eq 'OK') {
            $hours = $duration.Value.ToString([Globalization.CultureInfo]::InvariantCulture)
            Show-Status (Invoke-Control "pause -hours $hours")
        }
    } catch {
        [void][Windows.Forms.MessageBox]::Show($_.Exception.Message, 'Runner Orchard')
    } finally { $dialog.Dispose() }
})
$resume.Add_Click({
    try { Show-Status (Invoke-Control 'resume') }
    catch { [void][Windows.Forms.MessageBox]::Show($_.Exception.Message, 'Runner Orchard') }
})
$exit.Add_Click({ [Windows.Forms.Application]::ExitThread() })
$timer = [Windows.Forms.Timer]::new()
$timer.Interval = 5000
$timer.Add_Tick({ Refresh-Status })
try {
    Refresh-Status
    $timer.Start()
    [Windows.Forms.Application]::Run()
} finally {
    $timer.Dispose()
    $icon.Dispose()
    $menu.Dispose()
    $mutex.ReleaseMutex()
    $mutex.Dispose()
}
