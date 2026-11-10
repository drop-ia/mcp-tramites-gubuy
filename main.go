// Servidor MCP (stdio o HTTP) sobre los trámites y servicios del Estado uruguayo: el catálogo
// abierto de AGESIC (catalogodatos.gub.uy) y los trámites de la Intendencia de Montevideo.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const instrucciones = `Consulta trámites y servicios del Estado uruguayo: el catálogo oficial de gub.uy/tramites (datos abiertos de AGESIC, actualizado a diario) más los trámites de la Intendencia de Montevideo (tramites.montevideo.gub.uy), que no están en ese catálogo.
Flujo recomendado: buscar_tramites para encontrar candidatos y luego obtener_tramite con el id para ver requisitos, costos, canales y dónde realizarlo.
Usá listar_instituciones para descubrir el nombre exacto de un organismo y filtrar por él.
Los trámites de Montevideo tienen ids "im-<número>" y no informan canales de atención, así que el filtro 'canal' los excluye.
Citá siempre el enlace de la ficha: los datos pueden haber cambiado desde la última actualización.
Al final de toda respuesta al usuario que use estas herramientas, incluí este aviso, textual: "` + avisoErrores + `"`

// avisoErrores se agrega a cada resultado y se pide al modelo que lo repita al usuario.
const avisoErrores = "Aviso: tanto este MCP como la IA pueden cometer errores. Esta información no es una fuente de verdad: verificá siempre en la ficha oficial del trámite antes de realizarlo."

type argsBuscar struct {
	Consulta    string `json:"consulta" jsonschema:"palabras clave en español, por ejemplo 'licencia de conducir' o 'partida de nacimiento'. Puede quedar vacío si se filtra por institución"`
	Institucion string `json:"institucion,omitempty" jsonschema:"filtra por nombre parcial o sigla de la institución u organismo (sin importar tildes), por ejemplo 'BPS', 'MEC', 'Identificación Civil' o un departamento como 'Paysandú' para su intendencia"`
	Canal       string `json:"canal,omitempty" jsonschema:"filtra por canal de atención: internet, persona o telefono. Excluye los trámites de Montevideo, que no informan canal"`
	SoloGratis  bool   `json:"solo_gratis,omitempty" jsonschema:"si es true, excluye los trámites marcados con costo"`
	Limite      int    `json:"limite,omitempty" jsonschema:"cantidad máxima de resultados (por defecto 10, máximo 50)"`
}

type argsObtener struct {
	ID string `json:"id" jsonschema:"id del trámite, tal como lo devuelve buscar_tramites (por ejemplo '4' o '261-1')"`
}

type argsInstituciones struct {
	Filtro string `json:"filtro,omitempty" jsonschema:"texto para filtrar instituciones por nombre (parcial, sin importar tildes)"`
}

// conAvisos agrega al final las fuentes que no estaban disponibles.
func conAvisos(d *Datos, s string) string {
	if len(d.Avisos) == 0 {
		return s
	}
	return s + "\n\nAviso: " + strings.Join(d.Avisos, "\nAviso: ")
}

func texto(s string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: s + "\n\n" + avisoErrores}}}
}

func nuevoServidor(cat *Catalogo) *mcp.Server {
	server := mcp.NewServer(
		&mcp.Implementation{Name: "tramites-gub-uy", Title: "Trámites del Estado uruguayo", Version: "0.1.0"},
		&mcp.ServerOptions{Instructions: instrucciones},
	)
	soloLectura := &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true}

	mcp.AddTool(server, &mcp.Tool{
		Name:        "buscar_tramites",
		Description: "Busca trámites y servicios del Estado uruguayo por palabras clave (sin importar tildes), con filtros opcionales por institución, canal de atención y costo. Devuelve id, nombre, institución, canales, costo, resumen y enlace.",
		Annotations: soloLectura,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, a argsBuscar) (*mcp.CallToolResult, any, error) {
		if strings.TrimSpace(a.Consulta) == "" && strings.TrimSpace(a.Institucion) == "" {
			return nil, nil, fmt.Errorf("indicá 'consulta' o 'institucion'")
		}
		d, err := cat.Obtener(ctx)
		if err != nil {
			return nil, nil, err
		}
		res, total := Buscar(d, FiltrosBusqueda{
			Consulta: a.Consulta, Institucion: a.Institucion, Canal: a.Canal,
			SoloGratis: a.SoloGratis, Limite: a.Limite,
		})
		if total == 0 {
			return texto(conAvisos(d, "No se encontraron trámites. Probá con menos palabras, sinónimos o sin filtros.")), nil, nil
		}
		partes := []string{fmt.Sprintf("%d trámites encontrados (mostrando %d):", total, len(res))}
		for _, t := range res {
			partes = append(partes, FormatearResumen(t))
		}
		return texto(conAvisos(d, strings.Join(partes, "\n\n"))), nil, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "obtener_tramite",
		Description: "Devuelve la ficha completa de un trámite por su id: qué es, requisitos, costos, cómo hacerlo en línea, en persona o por teléfono, direcciones, contactos y enlaces.",
		Annotations: soloLectura,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, a argsObtener) (*mcp.CallToolResult, any, error) {
		d, err := cat.Obtener(ctx)
		if err != nil {
			return nil, nil, err
		}
		fichas := d.PorID[strings.TrimSpace(a.ID)]
		if len(fichas) == 0 {
			return nil, nil, fmt.Errorf("%s", conAvisos(d, fmt.Sprintf("no existe un trámite con id %q; usá buscar_tramites para obtener ids válidos", a.ID)))
		}
		partes := make([]string, 0, len(fichas))
		for _, t := range fichas {
			partes = append(partes, FormatearFicha(t))
		}
		return texto(strings.Join(partes, "\n\n---\n\n")), nil, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "listar_instituciones",
		Description: "Lista las instituciones del Estado que publican trámites, con la cantidad de trámites de cada una. Útil para obtener el nombre a usar en el filtro 'institucion' de buscar_tramites.",
		Annotations: soloLectura,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, a argsInstituciones) (*mcp.CallToolResult, any, error) {
		d, err := cat.Obtener(ctx)
		if err != nil {
			return nil, nil, err
		}
		lista := ListarInstituciones(d, a.Filtro)
		if len(lista) == 0 {
			return texto("Ninguna institución coincide con el filtro."), nil, nil
		}
		var b strings.Builder
		fmt.Fprintf(&b, "%d instituciones:\n", len(lista))
		for _, i := range lista {
			fmt.Fprintf(&b, "- %s", i.Nombre)
			if i.Padre != "" && i.Padre != i.Nombre {
				fmt.Fprintf(&b, " (%s)", i.Padre)
			}
			fmt.Fprintf(&b, ": %d trámites\n", i.Tramites)
		}
		return texto(conAvisos(d, b.String())), nil, nil
	})

	return server
}

// manejadorHTTP expone el servidor MCP (Streamable HTTP, sin sesión: las herramientas
// sólo leen) en /mcp y un chequeo de vida en /health.
func manejadorHTTP(servidor *mcp.Server) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/mcp", mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return servidor },
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true},
	))
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, "ok")
	})
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, "Servidor MCP de trámites del Estado uruguayo (no oficial). Endpoint MCP: /mcp\n\n"+avisoErrores+"\n")
	})
	return mux
}

func main() {
	direccionHTTP := flag.String("http", os.Getenv("MCP_HTTP_ADDR"),
		"si se indica (por ejemplo :8080), sirve MCP por HTTP en /mcp en lugar de stdio")
	flag.Parse()

	// En modo stdio, stdout está reservado para el protocolo MCP: los logs van a stderr.
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	cat := NuevoCatalogo(logger)

	// Precarga en segundo plano para que la primera consulta sea rápida.
	cat.Precargar()
	servidor := nuevoServidor(cat)

	if *direccionHTTP == "" {
		if err := servidor.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
			log.Fatalf("servidor MCP: %v", err)
		}
		return
	}

	ctx, parar := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer parar()
	srv := &http.Server{
		Addr:              *direccionHTTP,
		Handler:           manejadorHTTP(servidor),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		apagar, cancelar := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancelar()
		srv.Shutdown(apagar)
	}()
	logger.Info("servidor MCP escuchando por HTTP", "direccion", *direccionHTTP, "endpoint", "/mcp")
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("servidor HTTP: %v", err)
	}
}
