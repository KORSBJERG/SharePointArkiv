$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
$WarningPreference = 'SilentlyContinue'
$InformationPreference = 'SilentlyContinue'
[Console]::InputEncoding = New-Object System.Text.UTF8Encoding($false)
[Console]::OutputEncoding = New-Object System.Text.UTF8Encoding($false)

function Send-SetupReply($value) {
    [Console]::WriteLine('PEAKNET_SETUP ' + (ConvertTo-Json -InputObject $value -Depth 8 -Compress))
}

$stage = 'module'
try {
    $minimumVersion = [version]'2.25.0'
    if (-not (Get-Module -ListAvailable -Name Microsoft.Graph.Authentication |
            Where-Object { $_.Version -ge $minimumVersion } | Select-Object -First 1)) {
        Send-SetupReply @{ kind = 'progress'; stage = 'module' }
        [Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12
        $gallery = Get-PSRepository -Name PSGallery
        if ($gallery.SourceLocation.TrimEnd('/') -ne 'https://www.powershellgallery.com/api/v2') {
            throw 'The official PowerShell Gallery is unavailable.'
        }
        if (-not (Get-PackageProvider -Name NuGet -ListAvailable -ErrorAction SilentlyContinue)) {
            Install-PackageProvider -Name NuGet -MinimumVersion 2.8.5.201 -Scope CurrentUser -Force | Out-Null
        }
        Install-Module -Name Microsoft.Graph.Authentication -MinimumVersion $minimumVersion -Repository PSGallery -Scope CurrentUser -Force -Confirm:$false | Out-Null
    }

    Import-Module Microsoft.Graph.Authentication -MinimumVersion $minimumVersion
    $stage = 'login'
    Send-SetupReply @{ kind = 'progress'; stage = 'login' }
    $connect = @{
        Scopes = @('Application.ReadWrite.All', 'AppRoleAssignment.ReadWrite.All', 'Sites.FullControl.All', 'Sites.Read.All')
        ContextScope = 'Process'
        NoWelcome = $true
    }
    if ($env:PEAKNET_SETUP_TENANT) { $connect.TenantId = $env:PEAKNET_SETUP_TENANT }
    Connect-MgGraph @connect | Out-Null
    Set-MgRequestContext -ClientTimeout 20 -MaxRetry 0 | Out-Null
    Send-SetupReply @{ kind = 'ready'; tenant = (Get-MgContext).TenantId }

    while ($null -ne ($line = [Console]::ReadLine())) {
        try {
            $request = ConvertFrom-Json -InputObject $line
            $status = 0
            $arguments = @{
                Method = $request.method
                Uri = 'https://graph.microsoft.com/v1.0' + $request.path
                OutputType = 'Json'
                SkipHttpErrorCheck = $true
                StatusCodeVariable = 'status'
            }
            if ($request.body) { $arguments.Body = $request.body; $arguments.ContentType = 'application/json' }
            $response = Invoke-MgGraphRequest @arguments
            if ($status -ge 200 -and $status -lt 300) {
                Send-SetupReply @{ kind = 'response'; status = [int]$status; body = [string]$response }
            } else {
                $code = ''
                if ($response) {
                    try { $code = (ConvertFrom-Json -InputObject $response).error.code } catch {}
                }
                Send-SetupReply @{ kind = 'response'; status = [int]$status; code = $code }
            }
        } catch {
            Send-SetupReply @{ kind = 'request-error' }
        }
    }
} catch {
    $code = [regex]::Match($_.ToString(), 'AADSTS[0-9]+').Value
    Send-SetupReply @{ kind = 'startup-error'; stage = $stage; code = $code }
} finally {
    if (Get-Command Disconnect-MgGraph -ErrorAction SilentlyContinue) {
        Disconnect-MgGraph -ErrorAction SilentlyContinue | Out-Null
    }
}
