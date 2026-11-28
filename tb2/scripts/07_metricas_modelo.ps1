# Métricas del modelo y comparación de conjuntos de variables (ablation), todo con el clúster real.
#  - Validación (entrena 2018-2024, evalúa 2025): elige variables, pesos e hiperparámetros.
#  - Prueba final (entrena 2018-2025, evalúa 2026): se mide una sola vez cada conjunto.
# Al terminar deja vigente el modelo por defecto (tb2, 32 árboles).
. "$PSScriptRoot\_comun.ps1"
Connect-Api "admin" | Out-Null

$experimentos = @(
    # validación: variables
    @{ etapa = "validacion"; nombre = "base (variables reales)"; p = @{ mode = "validacion"; featureSet = "base" } },
    @{ etapa = "validacion"; nombre = "tb2 (reales + patrón 12 meses)"; p = @{ mode = "validacion"; featureSet = "tb2" } },
    # validación: pesos por clase
    @{ etapa = "validacion"; nombre = "tb2 + pesos sqrt"; p = @{ mode = "validacion"; featureSet = "tb2"; classWeight = "sqrt" } },
    @{ etapa = "validacion"; nombre = "tb2 + pesos balanced"; p = @{ mode = "validacion"; featureSet = "tb2"; classWeight = "balanced" } },
    # validación: hiperparámetros
    @{ etapa = "validacion"; nombre = "tb2 profundidad 16"; p = @{ mode = "validacion"; featureSet = "tb2"; maxDepth = 16 } },
    @{ etapa = "validacion"; nombre = "tb2 mtry 5"; p = @{ mode = "validacion"; featureSet = "tb2"; mtry = 5 } },
    @{ etapa = "validacion"; nombre = "tb2 hoja mínima 20"; p = @{ mode = "validacion"; featureSet = "tb2"; minLeaf = 20 } },
    @{ etapa = "validacion"; nombre = "tb2 64 árboles"; p = @{ mode = "validacion"; featureSet = "tb2"; trees = 64 } },
    # prueba final 2026
    @{ etapa = "prueba 2026"; nombre = "pc4 (con franja imputada: fuga)"; p = @{ featureSet = "pc4" } },
    @{ etapa = "prueba 2026"; nombre = "base (variables reales)"; p = @{ featureSet = "base" } },
    @{ etapa = "prueba 2026"; nombre = "tb2 + pesos sqrt"; p = @{ featureSet = "tb2"; classWeight = "sqrt" } },
    @{ etapa = "prueba 2026"; nombre = "tb2 (modelo final)"; p = @{ featureSet = "tb2" } }
)

$filas = @()
$detalle = @()
foreach ($e in $experimentos) {
    $t = Invoke-Train $e.p
    $ev = $t.Json.evaluation
    $f1 = [ordered]@{}
    foreach ($c in $ev.perClass) { $f1[$c.class] = [math]::Round($c.f1, 4) }
    $filas += [pscustomobject][ordered]@{
        etapa = $e.etapa; experimento = $e.nombre; variables = $t.Json.featureSet; pesos = $t.Json.config.classWeight
        profundidad = $t.Json.config.maxDepth; mtry = $t.Json.config.mtry; hojaMin = $t.Json.config.minLeaf; arboles = $t.Json.totalTrees
        anioEvaluado = $ev.evalYear; denuncias = $ev.testRecords
        exactitud = [math]::Round($ev.accuracy, 4); top3 = [math]::Round($ev.top3Accuracy, 4); f1Macro = [math]::Round($ev.macroF1, 4)
        f1Ponderado = [math]::Round($ev.weightedF1, 4); lineaBase = [math]::Round($ev.baselineAccuracy, 4); persistencia = [math]::Round($ev.persistenceAccuracy, 4)
    }
    $detalle += [ordered]@{ experimento = $e.nombre; etapa = $e.etapa; parametros = $e.p; version = $t.Json.version; features = $t.Json.features; evaluacion = $ev; f1PorClase = $f1 }
    Write-Host ("{0,-12} {1,-34} exactitud {2:P2}  top-3 {3:P2}  F1 macro {4:N3}" -f $e.etapa, $e.nombre, $ev.accuracy, $ev.top3Accuracy, $ev.macroF1)
}

$fin = $detalle[-1].evaluacion
$txt = @()
$txt += "MÉTRICAS DEL MODELO FINAL (tb2, 32 árboles, prueba 2026: $($fin.testRecords) denuncias)"
$txt += ("Exactitud {0:P2} | Top-3 {1:P2} | F1 macro {2:N4} | F1 ponderado {3:N4}" -f $fin.accuracy, $fin.top3Accuracy, $fin.macroF1, $fin.weightedF1)
$txt += ("Línea base (siempre {0}) {1:P2} | Persistencia (modalidad más frecuente de los 12 meses previos) {2:P2}" -f $fin.baselineClass, $fin.baselineAccuracy, $fin.persistenceAccuracy)
$txt += ""
$txt += "Por clase (precisión / exhaustividad / F1 / reales):"
foreach ($c in $fin.perClass) { $txt += ("  {0,-34} {1,7:P1} {2,7:P1} {3,7:N3} {4,9:N0}" -f $c.class, $c.precision, $c.recall, $c.f1, $c.real) }
$txt += ""
$txt += "Matriz de confusión (filas = real, columnas = predicha):"
$txt += ("  {0,-12}" -f "") + (($fin.classes | ForEach-Object { "{0,10}" -f $_.Substring(0, [math]::Min(9, $_.Length)) }) -join "")
for ($i = 0; $i -lt $fin.classes.Count; $i++) {
    $txt += ("  {0,-12}" -f $fin.classes[$i].Substring(0, [math]::Min(11, $fin.classes[$i].Length))) + (($fin.confusion[$i] | ForEach-Object { "{0,10:N0}" -f $_ }) -join "")
}
$txt += ""
$txt += "Importancia de variables (Gini):"
foreach ($f in ($fin.featureImportance | Sort-Object importance -Descending)) { $txt += ("  {0,-50} {1,6:P1}" -f $f.feature, $f.importance) }
$txt | ForEach-Object { Write-Host $_ }

Save-Json ([ordered]@{ fecha = (Get-Date).ToString("yyyy-MM-ddTHH:mm:ss"); resumen = $filas; detalle = $detalle }) "07_metricas.json"
Save-Csv $filas "07_metricas.csv"
Save-Text ([string[]]$txt) "07_metricas.txt"
