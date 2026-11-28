# Fuentes de datos del TB2

## 1. Denuncias policiales (dataset principal)

`DATASET_Denuncias Policiales - Enero 2018 a Septiembre 2026.csv`: copia exacta del CSV de la raíz del repositorio
(el mismo de la PC3 y la PC4). No se sube dentro de `tb2/` (está en `.gitignore`); `scripts\01_levantar.ps1` lo copia aquí si falta.

- Origen: Sistema de Denuncias Policiales (SIDPOL), Ministerio del Interior; Plataforma Nacional de Datos Abiertos del Perú
  (https://www.datosabiertos.gob.pe).
- 376,715 filas **agregadas** (distrito × mes × modalidad), 8 columnas: `ANIO, MES, DPTO_HECHO_NEW, PROV_HECHO, DIST_HECHO,
  UBIGEO_HECHO, P_MODALIDADES, cantidad`. La columna `cantidad` suma **7,509,690 denuncias** a nivel nacional.
- Alcance del modelo: Lima Metropolitana y Callao, 6 modalidades (sin "Otros") → **1,987,556 denuncias** desagregadas
  (entrenamiento 2018–2025: 1,823,335; prueba 2026: 164,221).
- El CSV **no** trae la hora ni el día de la semana del hecho. El `UBIGEO_HECHO` viene sin el cero inicial (`10202` → `010202`).
- SHA-256 (primeros 20 caracteres): `e67a1edcd84483df5c47`.

## 2. Población proyectada por distrito (INEI)

`6894980-peru-poblacion-total-proyectada-al-30-de-junio-de-cada-ano-segun-departamento-provincia-y-distrito-2018-2026.xlsx`
(sí se versiona: 211 KB).

- Origen: Instituto Nacional de Estadística e Informática (INEI), *Perú: Población total proyectada al 30 de junio de cada año,
  según departamento, provincia y distrito, 2018-2026*, publicado el 31/12/2025.
  https://www.gob.pe/institucion/inei/informes-publicaciones/6894980-peru-poblacion-total-proyectada-al-30-de-junio-de-cada-ano-segun-departamento-provincia-y-distrito-2018-2026
- Hoja única "POB. PROYECTADA 2018-2026": UBIGEO de 6 dígitos, nombre y población 2018–2026; **1,892 distritos**
  (el UBIGEO 160405, Santa Rosa de Loreto, viene como número y no como texto).
- 18 distritos creados entre 2020 y 2025 (ninguno de Lima ni del Callao) dicen "Distrito creado en 20XX" en los años previos:
  el lector (`internal/poblacion`) usa el año publicado más cercano.
- Cruce verificado: los 1,839 UBIGEO del CSV de denuncias tienen población (0 sin cruce).
- Uso: tasa de denuncias por 100,000 habitantes y población cubierta por el patrullaje priorizado (pregunta de impacto social).

## Referencias (APA 7)

- Instituto Nacional de Estadística e Informática. (2025). *Perú: Población total proyectada al 30 de junio de cada año, según
  departamento, provincia y distrito, 2018-2026* [Conjunto de datos]. Plataforma digital única del Estado peruano (gob.pe).
- Ministerio del Interior. (2026). *Denuncias policiales registradas – SIDPOL* [Conjunto de datos]. Plataforma Nacional de Datos
  Abiertos del Perú. https://www.datosabiertos.gob.pe
