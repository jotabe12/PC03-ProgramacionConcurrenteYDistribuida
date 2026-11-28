# Crea (o completa) el tablero Kanban del Grupo 3 en GitHub Projects: historial PC3 -> PC4 y tareas del TB2.
# Requisitos: gh autenticado como jotabe12 con los permisos "repo" y "project"  (gh auth refresh -s project).
# Es idempotente: si se ejecuta de nuevo no duplica etiquetas, hitos, issues ni el proyecto.
# Uso (desde la carpeta tb2):   .\scripts\00_kanban_github.ps1
param(
    [string]$Owner = "jotabe12",
    [string]$RepoName = "CC65-TB2-Grupo3-RandomForest-Denuncias",
    [string]$Titulo = "CC65 · Grupo 3 · Kanban del proyecto (PC3 → PC4 → TB2)"
)
$ErrorActionPreference = "Continue"
$ProgressPreference = "SilentlyContinue"
[Console]::OutputEncoding = [System.Text.Encoding]::UTF8
$Repo = "$Owner/$RepoName"
$Root = Split-Path $PSScriptRoot -Parent
$Utf8 = New-Object System.Text.UTF8Encoding $false

# Nota: PowerShell no distingue mayúsculas, así que la función NO puede llamarse "gh"
# (se llamaría a sí misma). Se invoca el ejecutable gh.exe de forma explícita.
$GhExe = (Get-Command gh.exe -CommandType Application -ErrorAction Stop | Select-Object -First 1).Source
function Invoke-Gh {
    # Ejecuta gh.exe y falla con un mensaje claro si el código de salida no es 0.
    $out = & $GhExe @args
    if ($LASTEXITCODE -ne 0) { throw "gh $($args -join ' ') terminó con código $LASTEXITCODE" }
    return $out
}
function Invoke-GhJson { (Invoke-Gh @args | Out-String) | ConvertFrom-Json }

Write-Host "Repositorio: $Repo" -ForegroundColor Cyan
Invoke-Gh repo view $Repo --json name | Out-Null

# ------------------------------------------------------------------ 1. Etiquetas
$Etiquetas = @(
    @("pc3", "0e8a16", "Entregable 1 (PC3)"), @("pc4", "1d76db", "Entregable 2 (PC4)"), @("tb2", "5319e7", "Entregable 3 (TB2)"),
    @("backend", "c5def5", "API, coordinador y nodos en Go"), @("frontend", "f9d0c4", "SPA React"),
    @("ml", "fbca04", "Modelo Random Forest distribuido"), @("infraestructura", "bfd4f2", "Docker Compose, redes y bases de datos"),
    @("seguridad", "b60205", "JWT, CORS, cuotas, aislamiento de red"), @("pruebas", "0052cc", "Pruebas y evaluación experimental"),
    @("documentacion", "d4c5f9", "Informe, README, video y sustentación"), @("gestion", "ededed", "Git Flow y Kanban")
)
foreach ($e in $Etiquetas) { Invoke-Gh label create $e[0] --repo $Repo --color $e[1] --description $e[2] --force | Out-Null }
Write-Host "Etiquetas listas ($($Etiquetas.Count))" -ForegroundColor Green

# ------------------------------------------------------------------ 2. Hitos (milestones)
$Hitos = @(
    @{ T = "PC3 · Entregable 1"; S = "closed"; D = "2026-11-08T04:30:00Z"; X = "Caso, limpieza concurrente, diseño del RF y paralelización (semana 11)" },
    @{ T = "PC4 · Entregable 2"; S = "closed"; D = "2026-11-22T04:30:00Z"; X = "Clúster ML, API REST, MongoDB/Redis y Docker (semana 13)" },
    @{ T = "TB2 · Entregable 3"; S = "open";   D = "2026-11-30T04:30:00Z"; X = "Frontend, seguridad, tiempo real, evaluación experimental, informe y video (semana 14)" }
)
# Se crean (o reabren) abiertos para poder asignarles issues; al final se cierran los de PC3 y PC4.
$existentes = Invoke-GhJson api "repos/$Repo/milestones?state=all&per_page=100"
foreach ($h in $Hitos) {
    $m = $existentes | Where-Object { $_.title -eq $h.T } | Select-Object -First 1
    if (-not $m) {
        Invoke-Gh api "repos/$Repo/milestones" -X POST -f "title=$($h.T)" -f "state=open" -f "due_on=$($h.D)" -f "description=$($h.X)" | Out-Null
    } elseif ($m.state -ne "open") {
        Invoke-Gh api "repos/$Repo/milestones/$($m.number)" -X PATCH -f "state=open" | Out-Null
    }
}
Write-Host "Hitos listos" -ForegroundColor Green

# ------------------------------------------------------------------ 3. Proyecto (tablero)
$lista = Invoke-GhJson project list --owner $Owner --format json --limit 100
$proj = $lista.projects | Where-Object { $_.title -eq $Titulo } | Select-Object -First 1
if (-not $proj) {
    $proj = Invoke-GhJson project create --owner $Owner --title $Titulo --format json
    Write-Host "Proyecto creado: #$($proj.number)" -ForegroundColor Green
} else {
    Write-Host "Proyecto existente: #$($proj.number)" -ForegroundColor Yellow
}
$Num = $proj.number
$ProjId = $proj.id
& $GhExe project edit $Num --owner $Owner --visibility PUBLIC --description "Seguimiento del trabajo final de CC65 (Grupo 3): Random Forest distribuido en Go para el patrullaje preventivo." | Out-Null
& $GhExe project link $Num --owner $Owner --repo $Repo 2>$null | Out-Null

# Campos propios: Entregable y Responsable (el campo Status ya existe: Todo / In Progress / Done).
$campos = (Invoke-GhJson project field-list $Num --owner $Owner --format json --limit 50).fields
if (-not ($campos | Where-Object { $_.name -eq "Entregable" })) {
    Invoke-Gh project field-create $Num --owner $Owner --name "Entregable" --data-type "SINGLE_SELECT" --single-select-options "PC3,PC4,TB2" | Out-Null
}
if (-not ($campos | Where-Object { $_.name -eq "Responsable" })) {
    Invoke-Gh project field-create $Num --owner $Owner --name "Responsable" --data-type "SINGLE_SELECT" --single-select-options "Juan Diego,Karim,Tomás,Equipo" | Out-Null
}
$campos = (Invoke-GhJson project field-list $Num --owner $Owner --format json --limit 50).fields
function Opcion([string]$campo, [string]$valor) {
    $f = $campos | Where-Object { $_.name -eq $campo } | Select-Object -First 1
    if (-not $f) { throw "No existe el campo $campo en el proyecto" }
    $o = $f.options | Where-Object { $_.name -eq $valor } | Select-Object -First 1
    if (-not $o) { throw "El campo $campo no tiene la opción $valor" }
    return @{ Field = $f.id; Option = $o.id }
}

# ------------------------------------------------------------------ 4. Tareas
# Responsables propuestos para el TB2: EDITAR la columna R antes de ejecutar si el grupo acordó otra distribución
# (luego también se pueden cambiar en el tablero).
$Tareas = @(
    # ---------------- PC3 (historial, cerradas)
    @{ T = "PC3 · Caso, dataset y pregunta de impacto social"; M = "PC3 · Entregable 1"; E = "PC3"; R = "Equipo"; S = "Done"; L = "pc3,documentacion"
       B = "Caso 2 del enunciado: clasificación de delitos (Denuncias Policiales, MININTER). Pregunta de impacto: dónde y para qué delitos priorizar el patrullaje preventivo en Lima Metropolitana y Callao." },
    @{ T = "PC3 · Carga y limpieza concurrente del CSV (más de 1 M de registros)"; M = "PC3 · Entregable 1"; E = "PC3"; R = "Tomás"; S = "Done"; L = "pc3,backend"
       B = "Pipeline productor-consumidor con canales y Worker Pool: 376,715 filas agregadas -> 1,987,556 denuncias de Lima y Callao." },
    @{ T = "PC3 · Diseño del Random Forest y paralelización con goroutines"; M = "PC3 · Entregable 1"; E = "PC3"; R = "Equipo"; S = "Done"; L = "pc3,ml"
       B = "Árboles CART con Gini entrenados en paralelo (Worker Pool), benchmark con 1 y N workers." },
    @{ T = "PC3 · Informe, repositorio y tablero Kanban"; M = "PC3 · Entregable 1"; E = "PC3"; R = "Tomás"; S = "Done"; L = "pc3,documentacion,gestion"
       B = "Informe del Entregable 1, repositorio inicial y documentación del tablero (commits del 08/11/2026)." },
    # ---------------- PC4 (historial, cerradas)
    @{ T = "PC4 · Clúster TCP de nodos ML (topología estrella) y RF distribuido"; M = "PC4 · Entregable 2"; E = "PC4"; R = "Karim"; S = "Done"; L = "pc4,backend,ml"
       B = "Coordinador TCP con JSON por línea, 3 nodos ML con shard estratificado y voto suave ponderado por árboles." },
    @{ T = "PC4 · API REST, Swagger y Postman (<100 ms)"; M = "PC4 · Entregable 2"; E = "PC4"; R = "Karim"; S = "Done"; L = "pc4,backend,pruebas"
       B = "Endpoints health, train, predict, patrullaje, districts y metrics; latencia promedio ~1.5 ms." },
    @{ T = "PC4 · Persistencia MongoDB + Redis y Docker Compose"; M = "PC4 · Entregable 2"; E = "PC4"; R = "Karim"; S = "Done"; L = "pc4,infraestructura"
       B = "Colecciones training_jobs, predictions, patrol_recommendations y cluster_metrics; caché con TTL; 6 contenedores." },
    @{ T = "PC4 · Correcciones (pc4-mejora), pruebas -race e informe"; M = "PC4 · Entregable 2"; E = "PC4"; R = "Juan Diego"; S = "Done"; L = "pc4,pruebas,documentacion"
       B = "Catálogo global por UBIGEO, sharding estratificado, respuestas tardías, caché versionada, 15 pruebas con -race e informe del Entregable 2." },
    # ---------------- TB2 (abiertas)
    @{ T = "TB2 · Estructura tb2/, Git Flow y tablero Kanban"; M = "TB2 · Entregable 3"; E = "TB2"; R = "Juan Diego"; S = "In Progress"; L = "tb2,gestion"
       B = "Carpeta tb2/ desde pc4-mejora, tags v1.0-pc3 y v2.0-pc4, rama develop, ramas feature/* con PR y este tablero." },
    @{ T = "TB2 · Seguridad de la API: JWT, roles, CORS y cuotas"; M = "TB2 · Entregable 3"; E = "TB2"; R = "Juan Diego"; S = "Todo"; L = "tb2,backend,seguridad"
       B = "Login con JWT HS256, contraseñas con PBKDF2, roles admin/analista, CORS con lista blanca, límite de intentos y cuotas por rol." },
    @{ T = "TB2 · Tiempo real: WebSocket, CPU por nodo, latencias y logs"; M = "TB2 · Entregable 3"; E = "TB2"; R = "Juan Diego"; S = "Todo"; L = "tb2,backend"
       B = "WebSocket (RFC 6455) con canales metrics, training, predictions y logs; CPU y memoria por nodo; buffer de logs del clúster." },
    @{ T = "TB2 · Modelo corregido: sin franja imputada y con patrón criminológico reciente"; M = "TB2 · Entregable 3"; E = "TB2"; R = "Karim"; S = "Todo"; L = "tb2,ml"
       B = "Quitar la franja imputada (fuga de etiqueta), agregar la mezcla de delitos de los últimos 12 meses del distrito, validar con 2025 y probar con 2026." },
    @{ T = "TB2 · Impacto social: población INEI, tasa por 100 mil y patrullaje priorizado"; M = "TB2 · Entregable 3"; E = "TB2"; R = "Tomás"; S = "Todo"; L = "tb2,backend,ml"
       B = "Endpoint /impacto: ranking de distritos, población cubierta y verificación contra los datos reales de 2026." },
    @{ T = "TB2 · Frontend: login, menú y dashboard de impacto"; M = "TB2 · Entregable 3"; E = "TB2"; R = "Tomás"; S = "Todo"; L = "tb2,frontend"
       B = "SPA en React: inicio de sesión, menú lateral por rol y panel de impacto social con KPIs, ranking y mapa de calor." },
    @{ T = "TB2 · Frontend: predicción, patrullaje y entrenamiento en vivo"; M = "TB2 · Entregable 3"; E = "TB2"; R = "Karim"; S = "Todo"; L = "tb2,frontend"
       B = "Formularios con parámetros desde la UI y progreso del entrenamiento por WebSocket." },
    @{ T = "TB2 · Frontend: evaluación del modelo y panel de administración"; M = "TB2 · Entregable 3"; E = "TB2"; R = "Juan Diego"; S = "Todo"; L = "tb2,frontend"
       B = "Matriz de confusión, F1 por clase, top-3; panel admin con nodos, CPU, latencias y logs en tiempo real." },
    @{ T = "TB2 · Docker Compose: 7 contenedores y redes segmentadas"; M = "TB2 · Entregable 3"; E = "TB2"; R = "Juan Diego"; S = "Todo"; L = "tb2,infraestructura,seguridad"
       B = "Redes frontend, cluster y datos (internas), Mongo con usuario restringido por IP, Redis con ACL y solo la UI publicada en 127.0.0.1." },
    @{ T = "TB2 · Experimento: speedup con 1-3 nodos y 1-8 workers"; M = "TB2 · Entregable 3"; E = "TB2"; R = "Karim"; S = "Todo"; L = "tb2,pruebas,ml"
       B = "Tiempos de carga, expansión y entrenamiento; speedup y eficiencia; discusión con la ley de Amdahl." },
    @{ T = "TB2 · Experimento: métricas del modelo y comparación PC4 vs TB2"; M = "TB2 · Entregable 3"; E = "TB2"; R = "Karim"; S = "Todo"; L = "tb2,pruebas,ml"
       B = "Accuracy, F1 macro, F1 por clase, matriz de confusión, top-3 y ablation de variables." },
    @{ T = "TB2 · Experimento: carga concurrente de la API (<100 ms)"; M = "TB2 · Entregable 3"; E = "TB2"; R = "Juan Diego"; S = "Todo"; L = "tb2,pruebas"
       B = "Generador de carga en Go con 10, 50 y 100 clientes: p50, p95, p99 y throughput." },
    @{ T = "TB2 · Pruebas end to end y de seguridad"; M = "TB2 · Entregable 3"; E = "TB2"; R = "Juan Diego"; S = "Todo"; L = "tb2,pruebas,seguridad"
       B = "Login -> entrenamiento -> predicción -> WebSocket -> MongoDB/Redis; casos 401, 403, 400, 404, 429, CORS y aislamiento de red." },
    @{ T = "TB2 · Respuesta documentada a la pregunta de impacto social"; M = "TB2 · Entregable 3"; E = "TB2"; R = "Tomás"; S = "Todo"; L = "tb2,pruebas,documentacion"
       B = "Distritos priorizados por mes, población cubierta y porcentaje de delitos de calle que concentra el patrullaje sugerido." },
    @{ T = "TB2 · Capturas y evidencias"; M = "TB2 · Entregable 3"; E = "TB2"; R = "Equipo"; S = "Todo"; L = "tb2,pruebas"
       B = "Capturas de la UI, Swagger, Postman, Docker, MongoDB, Redis, Git Flow y este tablero." },
    @{ T = "TB2 · Informe final (entregables 3 y 4 con correcciones)"; M = "TB2 · Entregable 3"; E = "TB2"; R = "Equipo"; S = "Todo"; L = "tb2,documentacion"
       B = "Informe en Word con la estructura del enunciado, registro de correcciones, Git Flow, Kanban y reporte de participación." },
    @{ T = "TB2 · Revisión cruzada contra la rúbrica"; M = "TB2 · Entregable 3"; E = "TB2"; R = "Equipo"; S = "Todo"; L = "tb2,documentacion"
       B = "Cotejar informe, código y evidencias con cada criterio de la rúbrica antes de subir." },
    @{ T = "TB2 · Video (6 min máx.) y entrega en el aula virtual"; M = "TB2 · Entregable 3"; E = "TB2"; R = "Equipo"; S = "Todo"; L = "tb2,documentacion"
       B = "Video end to end con los tres integrantes; cada integrante sube su informe y su ZIP." },
    @{ T = "TB2 · Preparación de la sustentación (semana 15)"; M = "TB2 · Entregable 3"; E = "TB2"; R = "Equipo"; S = "Todo"; L = "tb2,documentacion"
       B = "Guía de sustentación: datos clave, parte de cada integrante, preguntas probables y glosario." }
)

$issues = Invoke-GhJson issue list --repo $Repo --state all --limit 500 --json "number,title,url,state"
$resumen = @()
$tmp = [System.IO.Path]::GetTempFileName()
$i = 0
foreach ($t in $Tareas) {
    $i++
    $existe = $issues | Where-Object { $_.title -eq $t.T } | Select-Object -First 1
    if ($existe) {
        $url = $existe.url
        $issueNum = $existe.number
    } else {
        [System.IO.File]::WriteAllText($tmp, $t.B, $Utf8)
        $url = (Invoke-Gh issue create --repo $Repo --title $t.T --body-file $tmp --label $t.L --milestone $t.M --assignee $Owner | Select-Object -Last 1).Trim()
        $issueNum = [int]($url -split "/")[-1]
    }
    $item = Invoke-GhJson project item-add $Num --owner $Owner --url $url --format json
    foreach ($par in @(@("Status", $t.S), @("Entregable", $t.E), @("Responsable", $t.R))) {
        $o = Opcion $par[0] $par[1]
        Invoke-Gh project item-edit --id $item.id --project-id $ProjId --field-id $o.Field --single-select-option-id $o.Option | Out-Null
    }
    if ($t.S -eq "Done" -and (-not $existe -or $existe.state -ne "CLOSED")) {
        Invoke-Gh issue close $issueNum --repo $Repo --reason completed | Out-Null
    }
    $resumen += [pscustomobject]@{ issue = $issueNum; titulo = $t.T; estado = $t.S; responsable = $t.R; url = $url }
    Write-Host ("[{0,2}/{1}] #{2} {3} ({4})" -f $i, $Tareas.Count, $issueNum, $t.T, $t.S)
}
Remove-Item $tmp -ErrorAction SilentlyContinue

# Cerrar los hitos ya entregados (PC3 y PC4).
$existentes = Invoke-GhJson api "repos/$Repo/milestones?state=all&per_page=100"
foreach ($h in ($Hitos | Where-Object { $_.S -eq "closed" })) {
    $m = $existentes | Where-Object { $_.title -eq $h.T } | Select-Object -First 1
    if ($m -and $m.state -ne "closed") { Invoke-Gh api "repos/$Repo/milestones/$($m.number)" -X PATCH -f "state=closed" | Out-Null }
}

$evid = Join-Path $Root "evidencias"
New-Item -ItemType Directory -Force -Path $evid | Out-Null
$salida = [ordered]@{ proyecto = $Titulo; numero = $Num; url = "https://github.com/users/$Owner/projects/$Num"; repositorio = $Repo; tareas = $resumen }
[System.IO.File]::WriteAllText((Join-Path $evid "00_kanban_issues.json"), ($salida | ConvertTo-Json -Depth 5), $Utf8)

Write-Host ""
$resumen | Format-Table issue, estado, responsable, titulo -AutoSize
Write-Host "Tablero: https://github.com/users/$Owner/projects/$Num" -ForegroundColor Yellow
Write-Host "Falta un paso manual: en el tablero, menú de la vista -> Layout: Board (agrupado por Status)." -ForegroundColor Yellow
