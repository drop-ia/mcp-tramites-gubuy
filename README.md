# mcp-tramites-gub-uy

Servidor [MCP](https://modelcontextprotocol.io) en Go para consultar los trámites y servicios del Estado uruguayo.

Fuentes:

| Fuente | Contenido | Origen |
|---|---|---|
| [Catálogo de trámites y servicios del Estado](https://catalogodatos.gub.uy/dataset/agesic-guia-de-tramites) (AGESIC) | Unos 3.500 trámites de 166 organismos ([gub.uy/tramites](https://www.gub.uy/tramites)) | Dataset abierto en CSV, actualizado a diario |
| [Intendencia de Montevideo](https://tramites.montevideo.gub.uy) | Unos 500 trámites publicados, que no están en el catálogo de AGESIC | API JSON:API pública del portal (Drupal) |

La API de la IM no está publicada como dato abierto ni tiene documentación, así que puede cambiar sin aviso. Si falla, el servidor sigue respondiendo con los datos de AGESIC y agrega un aviso.

> **Aviso:** tanto este MCP como la IA pueden cometer errores. La información no es una fuente de verdad: verificá siempre en la ficha oficial del trámite antes de realizarlo. Cada resultado de las herramientas termina con este aviso, y las instrucciones del servidor le piden al modelo que lo repita al usuario.

## Herramientas

| Herramienta | Qué hace |
|---|---|
| `buscar_tramites` | Busca por palabras clave, sin importar tildes. Filtros: `institucion` (nombre parcial o sigla: `BPS`, `MEC`, `IM`), `canal` (`internet` / `persona` / `telefono`), `solo_gratis`, `limite`. |
| `obtener_tramite` | Devuelve la ficha completa por `id`: requisitos, costos, cómo se hace, direcciones, contactos y enlaces. Si un id tiene varias casuísticas, las devuelve todas. Los trámites de la IM usan ids `im-<número>`. |
| `listar_instituciones` | Lista los organismos y cuántos trámites tiene cada uno. |

## Compilar

```sh
go build -o bin/mcp-tramites-gub-uy .
go test ./...
```

## Configurar en un cliente MCP

Claude Code:

```sh
claude mcp add tramites-gub-uy -- /ruta/absoluta/bin/mcp-tramites-gub-uy
```

Claude Desktop y otros clientes (`mcpServers`):

```json
{
  "mcpServers": {
    "tramites-gub-uy": { "command": "/ruta/absoluta/bin/mcp-tramites-gub-uy" }
  }
}
```

## Modo HTTP y Docker

Con `-http :8080` (o la variable `MCP_HTTP_ADDR`) el servidor atiende MCP por HTTP en `/mcp`, sin sesión porque las herramientas solo leen, en lugar de stdio. También expone `GET /health` para el healthcheck.

```sh
docker build -t mcp-tramites-gub-uy .
docker run -p 8080:8080 -v tramites-cache:/data mcp-tramites-gub-uy
# endpoint: http://localhost:8080/mcp
```

La imagen pesa unos 8 MB, corre con un usuario sin privilegios y guarda la caché en el volumen `/data` (`TRAMITES_CACHE_DIR`), así que un redeploy arranca sin volver a descargar.

### Despliegue en Dokploy (tramites.dropia.uy)

En el servidor de dropia (`144.217.88.113`, donde ya apunta el DNS de `tramites.dropia.uy`):

1. Crear una aplicación desde el repo de GitHub, con build type **Dockerfile**.
2. **Domains:** `tramites.dropia.uy`, puerto `8080`, HTTPS con Let's Encrypt.
3. **Advanced → Volumes:** montar un volumen en `/data`.
4. No hacen falta variables de entorno.

El conector queda en `https://tramites.dropia.uy/mcp`.

## Datos y caché

- Al arrancar, cada fuente se descarga en segundo plano y se guarda en `$(os.UserCacheDir)/mcp-tramites-gub-uy/` (en macOS: `~/Library/Caches/...`): `tramites.csv` (unos 10 MB) y `montevideo.json`. Pasadas 24 h se vuelven a descargar, sin dejar de responder con la copia anterior.
- La IM se recorre en unas 11 páginas, y la primera descarga puede tardar más de un minuto. Una consulta espera a lo sumo 20 s; si no llega, responde sin la IM y con un aviso.
- Si una descarga falla, se sigue usando la última copia en disco, aunque tenga más de 24 h.
- Para usar otras URLs, definí `TRAMITES_CSV_URL` (CSV de AGESIC) o `TRAMITES_IM_URL` (base del portal de la IM).
- Los logs van a stderr; stdout queda reservado para el protocolo MCP.

## Limitaciones del dataset

- No todos los organismos publican en el catálogo de AGESIC. La Intendencia de Montevideo se agrega aparte; de las demás intendencias solo están las 11 que publican ahí.
- AGESIC informa el costo como `tiene_costo` más una unidad y un valor (U.R., U.I., $), a menudo vacíos o `Sin definir`. La IM lo informa como texto libre, y solo los que empiezan con "No tiene costo" cuentan como gratuitos.
- Los trámites de la IM no traen canales de atención: el campo de origen no coincide con lo que muestra el portal. Por eso el filtro `canal` los excluye; el detalle está en la sección "¿Cómo se hace?".
