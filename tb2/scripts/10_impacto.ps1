# Respuesta documentada a la pregunta de impacto social:
# "¿En qué distritos de Lima Metropolitana y Callao, en qué mes y para qué delitos de calle conviene
#  priorizar el patrullaje preventivo, y a cuántas personas cubre esa priorización?"
# Verificación (backtest) de enero a septiembre de 2026 contra las denuncias reales, y pronóstico
# de octubre a diciembre de 2026, con el top-10 de cada mes.
param([int]$Top = 10)
. "$PSScriptRoot\_comun.ps1"
Connect-Api "admin" | Out-Null
$h = Invoke-Api GET "/api/v1/health" -SinToken
if ($h.Json.model.featureSet -eq "pc4") { Invoke-Train @{} | Out-Null }
$meses = @("enero", "febrero", "marzo", "abril", "mayo", "junio", "julio", "agosto", "septiembre", "octubre", "noviembre", "diciembre")

$filas = @()
$reportes = @()
foreach ($m in 1..12) {
    $i = Invoke-Api GET "/api/v1/impacto?mes=$m&anio=2026&top=$Top&orden=casos"
    if ($i.Status -ne 200) { throw "impacto $m/2026: HTTP $($i.Status) $($i.Raw)" }
    $d = $i.Json
    $bt = $d.backtest
    $top3 = ($d.rows | Select-Object -First 3 | ForEach-Object { $_.district }) -join ", "
    $calle = ($d.rows | Select-Object -First $Top | Group-Object topStreetCrime | Sort-Object Count -Descending | ForEach-Object { "$($_.Name) ($($_.Count))" }) -join ", "
    $filas += [pscustomobject][ordered]@{
        mes = $meses[$m - 1]; tipo = $(if ($bt) { "verificación" } else { "pronóstico" })
        distritosTop = $Top; poblacionCubierta = $d.summary.populationTop; poblacionPct = [math]::Round($d.summary.populationPct, 1)
        calleEsperadosTop = [math]::Round($d.summary.expectedStreetTop); coberturaEsperadaPct = [math]::Round($d.summary.expectedCoveragePct, 1)
        calleRealesTop = $(if ($bt) { $bt.realStreetTop } else { $null }); calleRealesTotal = $(if ($bt) { $bt.realStreetTotal } else { $null })
        coberturaRealPct = $(if ($bt) { [math]::Round($bt.realCoveragePct, 1) } else { $null })
        coberturaOptimaPct = $(if ($bt) { [math]::Round($bt.oracleCoveragePct, 1) } else { $null })
        coincidenTop = $(if ($bt) { $bt.overlapAtN } else { $null })
        primeros3 = $top3; delitoCalleProbable = $calle
    }
    $reportes += [ordered]@{ mes = $m; resumen = $d.summary; backtest = $bt; top = @($d.rows | Select-Object -First $Top) }
}
$filas | Format-Table mes, tipo, poblacionPct, coberturaEsperadaPct, coberturaRealPct, coberturaOptimaPct, coincidenTop, primeros3 -AutoSize

$ver = $filas | Where-Object { $_.tipo -eq "verificación" }
$real = ($ver | Measure-Object calleRealesTop -Sum).Sum
$tot = ($ver | Measure-Object calleRealesTotal -Sum).Sum
$prom = ($ver | Measure-Object coberturaRealPct -Average).Average
$opt = ($ver | Measure-Object coberturaOptimaPct -Average).Average
$coinc = ($ver | Measure-Object coincidenTop -Average).Average
$pobl = ($filas | Measure-Object poblacionCubierta -Average).Average
$frecuentes = $reportes | ForEach-Object { $_.top } | Group-Object district | Sort-Object Count -Descending | Select-Object -First $Top
$txt = @()
$txt += "PREGUNTA DE IMPACTO SOCIAL"
$txt += "¿En qué distritos de Lima Metropolitana y Callao, en qué mes y para qué delitos de calle conviene priorizar el patrullaje preventivo, y a cuántas personas cubre esa priorización?"
$txt += ""
$txt += "RESPUESTA (modelo v$($h.Json.model.version), Random Forest distribuido de $($h.Json.model.totalTrees) árboles, población INEI)"
$txt += ("- Distritos que aparecen en el top-{0} de más meses de 2026: {1}." -f $Top, (($frecuentes | ForEach-Object { "$($_.Name) ($($_.Count) meses)" }) -join ", "))
$txt += ("- Priorizar {0} distritos cubre en promedio a {1:N0} habitantes por mes ({2:N1} % de Lima y Callao)." -f $Top, $pobl, (($filas | Measure-Object poblacionPct -Average).Average))
$txt += ("- Verificación enero-septiembre 2026: los distritos priorizados concentraron {0:N0} de {1:N0} delitos de calle reales ({2:N1} % en promedio por mes; el máximo posible con {3} distritos era {4:N1} %)." -f $real, $tot, $prom, $Top, $opt)
$txt += ("- En promedio, {0:N1} de los {1} distritos priorizados coincidieron con el top real de cada mes." -f $coinc, $Top)
$delitos = $reportes | ForEach-Object { $_.top } | Group-Object topStreetCrime | Sort-Object Count -Descending
$txt += ("- Delito de calle a vigilar (el más probable según el bosque) en los distritos priorizados de los 12 meses: {0}." -f (($delitos | ForEach-Object { "$($_.Name) en $($_.Count) casos" }) -join ", "))
$txt += "- La franja horaria sugerida es un supuesto (el CSV no registra la hora del hecho) y no forma parte de la respuesta del modelo."
$txt += ""
$txt += "Detalle por mes:"
foreach ($f in $filas) {
    $extra = ""
    if ($f.tipo -eq "verificación") { $extra = (" | real {0:N1} % (óptimo {1:N1} %), coinciden {2}/{3}" -f $f.coberturaRealPct, $f.coberturaOptimaPct, $f.coincidenTop, $Top) }
    $txt += ("  {0,-10} {1,-12} cubre {2:N0} hab. ({3:N1} %), {4:N1} % de los delitos de calle esperados{5}. Primeros: {6}" -f $f.mes, $f.tipo, $f.poblacionCubierta, $f.poblacionPct, $f.coberturaEsperadaPct, $extra, $f.primeros3)
}
$txt | ForEach-Object { Write-Host $_ }
Save-Json ([ordered]@{ fecha = (Get-Date).ToString("yyyy-MM-ddTHH:mm:ss"); top = $Top; resumen = $filas; meses = $reportes }) "10_impacto.json"
Save-Csv $filas "10_impacto.csv"
Save-Text ([string[]]$txt) "10_impacto.txt"
