# Funciones comunes de los scripts del TB2 (compatibles con Windows PowerShell 5.1 y PowerShell 7).
# "Continue": en Windows PowerShell 5.1, "Stop" convierte la salida de error de docker en excepción.
$ErrorActionPreference = "Continue"
$ProgressPreference = "SilentlyContinue"
[Console]::OutputEncoding = [System.Text.Encoding]::UTF8
$Root = Split-Path $PSScriptRoot -Parent
$Evid = Join-Path $Root "evidencias"
$Base = "http://localhost:8080"
$Utf8 = New-Object System.Text.UTF8Encoding $false
New-Item -ItemType Directory -Force -Path $Evid | Out-Null
Set-Location $Root

Add-Type -AssemblyName System.Net.Http
$script:Client = New-Object System.Net.Http.HttpClient
$script:Client.Timeout = [TimeSpan]::FromMinutes(10)
$script:Token = $null

# Lee .env (clave=valor) en un diccionario.
function Read-DotEnv {
    $ruta = Join-Path $Root ".env"
    $d = @{}
    if (Test-Path $ruta) {
        foreach ($l in [System.IO.File]::ReadAllLines($ruta)) {
            if ($l -match '^\s*([A-Za-z_][A-Za-z0-9_]*)\s*=\s*(.*)$') { $d[$Matches[1]] = $Matches[2].Trim() }
        }
    }
    return $d
}

# Llama a la API y mide el tiempo en el cliente (Stopwatch) y en el servidor (cabecera X-Response-Time-Ms).
# Usa el token de Connect-Api salvo que se indique -Token o -SinToken.
function Invoke-Api {
    param([string]$Method, [string]$Path, $Body = $null, [string]$Token = $null, [switch]$SinToken, [hashtable]$Headers = @{})
    $req = New-Object System.Net.Http.HttpRequestMessage ([System.Net.Http.HttpMethod]::new($Method)), "$Base$Path"
    if ($null -ne $Body) {
        if ($Body -is [string]) { $json = $Body } else { $json = $Body | ConvertTo-Json -Depth 10 -Compress }
        $req.Content = New-Object System.Net.Http.StringContent $json, ([System.Text.Encoding]::UTF8), "application/json"
    }
    $tok = $script:Token
    if ($Token) { $tok = $Token }
    if ($tok -and -not $SinToken) { [void]$req.Headers.TryAddWithoutValidation("Authorization", "Bearer $tok") }
    foreach ($k in $Headers.Keys) { [void]$req.Headers.TryAddWithoutValidation($k, $Headers[$k]) }
    $sw = [System.Diagnostics.Stopwatch]::StartNew()
    $resp = $script:Client.SendAsync($req).GetAwaiter().GetResult()
    $text = $resp.Content.ReadAsStringAsync().GetAwaiter().GetResult()
    $sw.Stop()
    $serverMs = $null
    $vals = $null
    if ($resp.Headers.TryGetValues("X-Response-Time-Ms", [ref]$vals)) { $serverMs = [double]($vals | Select-Object -First 1) }
    $obj = $null
    if ($text) { try { $obj = $text | ConvertFrom-Json } catch { $obj = $null } }
    $hdr = @{}
    foreach ($h in $resp.Headers) { $hdr[$h.Key] = ($h.Value -join ", ") }
    [pscustomobject]@{
        Status   = [int]$resp.StatusCode
        ClientMs = [math]::Round($sw.Elapsed.TotalMilliseconds, 3)
        ServerMs = $serverMs
        Json     = $obj
        Raw      = $text
        Headers  = $hdr
    }
}

# Inicia sesión y deja el token para las llamadas siguientes. Devuelve el token.
function Connect-Api {
    param([string]$Rol = "admin")
    $envs = Read-DotEnv
    if ($Rol -eq "admin") { $u = $envs["ADMIN_USER"]; $p = $envs["ADMIN_PASSWORD"] } else { $u = $envs["ANALISTA_USER"]; $p = $envs["ANALISTA_PASSWORD"] }
    if (-not $u) { throw "No hay credenciales en .env: ejecute primero scripts\01_levantar.ps1" }
    $r = Invoke-Api POST "/api/v1/auth/login" @{ username = $u; password = $p } -SinToken
    if ($r.Status -ne 200) { throw "Login de $u falló: HTTP $($r.Status) $($r.Raw)" }
    if ($Rol -eq "admin") { $script:Token = $r.Json.token }
    return $r.Json.token
}

function Get-Resumen {
    param([double[]]$Valores)
    if (-not $Valores -or $Valores.Count -eq 0) { return $null }
    $o = $Valores | Sort-Object
    $n = $o.Count
    [ordered]@{
        n               = $n
        promedioMs      = [math]::Round(($o | Measure-Object -Average).Average, 3)
        minimoMs        = [math]::Round($o[0], 3)
        p50Ms           = [math]::Round($o[[int][math]::Floor(0.50 * ($n - 1))], 3)
        p95Ms           = [math]::Round($o[[int][math]::Floor(0.95 * ($n - 1))], 3)
        maximoMs        = [math]::Round($o[$n - 1], 3)
        todosMenor100ms = ($o[$n - 1] -lt 100)
    }
}

function Get-Entorno {
    $cpu = Get-CimInstance Win32_Processor | Select-Object -First 1
    $cs = Get-CimInstance Win32_ComputerSystem
    $os = Get-CimInstance Win32_OperatingSystem
    $dockerServer = (docker version --format "{{.Server.Version}}" 2>$null)
    $dockerInfo = (docker info --format "{{.NCPU}} CPU, {{.MemTotal}} bytes, {{.OperatingSystem}}" 2>$null)
    $compose = (docker compose version --short 2>$null)
    [ordered]@{
        equipo           = $env:COMPUTERNAME
        sistemaOperativo = "$($os.Caption) $($os.Version)"
        cpu              = $cpu.Name.Trim()
        nucleosFisicos   = $cpu.NumberOfCores
        hilosLogicos     = $cpu.NumberOfLogicalProcessors
        ramGB            = [math]::Round($cs.TotalPhysicalMemory / 1GB, 1)
        docker           = "Docker Engine $dockerServer; Compose $compose"
        dockerVM         = $dockerInfo
        despliegue       = "Local: Docker Desktop en el equipo del integrante (no en la nube)"
    }
}

function Save-Json {
    param($Objeto, [string]$Archivo)
    $ruta = Join-Path $Evid $Archivo
    [System.IO.File]::WriteAllText($ruta, ($Objeto | ConvertTo-Json -Depth 30), $Utf8)
    Write-Host "  -> guardado evidencias\$Archivo" -ForegroundColor DarkGray
}

function Save-Csv {
    param($Filas, [string]$Archivo)
    $ruta = Join-Path $Evid $Archivo
    $lineas = $Filas | ConvertTo-Csv -NoTypeInformation
    [System.IO.File]::WriteAllLines($ruta, [string[]]$lineas, $Utf8)
    Write-Host "  -> guardado evidencias\$Archivo" -ForegroundColor DarkGray
}

function Save-Text {
    param([string[]]$Lineas, [string]$Archivo)
    [System.IO.File]::WriteAllLines((Join-Path $Evid $Archivo), $Lineas, $Utf8)
    Write-Host "  -> guardado evidencias\$Archivo" -ForegroundColor DarkGray
}

# Como Tee-Object (muestra cada línea y la guarda), pero en UTF-8: el Tee-Object de
# Windows PowerShell 5.1 escribe UTF-16 y GitHub muestra esos archivos como binarios.
function Save-Tee {
    param([Parameter(Mandatory)][string]$Ruta, [Parameter(ValueFromPipeline)]$Linea)
    begin { $lineasTee = New-Object System.Collections.Generic.List[string] }
    process { $texto = "$Linea"; $lineasTee.Add($texto); $texto }
    end { [System.IO.File]::WriteAllLines($Ruta, $lineasTee.ToArray(), $Utf8) }
}

function Wait-Api {
    param([scriptblock]$Condicion, [int]$Segundos = 180, [string]$Mensaje = "esperando")
    $limite = (Get-Date).AddSeconds($Segundos)
    while ((Get-Date) -lt $limite) {
        try {
            $h = Invoke-Api GET "/api/v1/health" -SinToken
            if ($h.Status -eq 200 -and (& $Condicion $h.Json)) { return $h }
        } catch { }
        Write-Host "  ... $Mensaje" -ForegroundColor DarkGray
        Start-Sleep -Seconds 2
    }
    throw "Tiempo agotado: $Mensaje"
}

# Entrena y espera la respuesta (el entrenamiento es síncrono en la API).
function Invoke-Train {
    param([hashtable]$Parametros)
    $t = Invoke-Api POST "/api/v1/train" $Parametros
    if ($t.Status -ne 200) { throw "Entrenamiento falló: HTTP $($t.Status) $($t.Raw)" }
    return $t
}
