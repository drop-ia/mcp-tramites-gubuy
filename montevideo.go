package main

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// La Intendencia de Montevideo no publica su guía de trámites en el catálogo de datos
// abiertos ni en la guía de AGESIC. Se lee de la API JSON:API (Drupal) del portal
// tramites.montevideo.gub.uy, que es pública pero no está documentada como dato abierto:
// su estructura puede cambiar sin aviso.
const urlIMPorDefecto = "https://tramites.montevideo.gub.uy"

const nombreIM = "Intendencia de Montevideo"

type jsonAPIRespuesta struct {
	Data     []jsonAPIRecurso `json:"data"`
	Included []jsonAPIRecurso `json:"included"`
	Links    struct {
		Next *struct {
			Href string `json:"href"`
		} `json:"next"`
	} `json:"links"`
}

type jsonAPIRecurso struct {
	Type          string                     `json:"type"`
	ID            string                     `json:"id"`
	Attributes    map[string]json.RawMessage `json:"attributes"`
	Relationships map[string]struct {
		Data json.RawMessage `json:"data"`
	} `json:"relationships"`
}

type refRecurso struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

func fuenteMontevideo(base, rutaCache string, cliente *http.Client, log *slog.Logger) *fuente {
	return &fuente{
		nombre:    nombreIM,
		rutaCache: rutaCache,
		log:       log,
		descargar: func(ctx context.Context) ([]byte, error) {
			ts, err := descargarMontevideo(ctx, cliente, base)
			if err != nil {
				return nil, err
			}
			return json.Marshal(ts)
		},
		parsear: func(crudo []byte) ([]*Tramite, error) {
			var ts []*Tramite
			if err := json.Unmarshal(crudo, &ts); err != nil {
				return nil, err
			}
			if len(ts) == 0 {
				return nil, fmt.Errorf("sin trámites de %s", nombreIM)
			}
			for _, t := range ts {
				t.indexar()
			}
			return ts, nil
		},
	}
}

// descargarMontevideo recorre todas las páginas de trámites publicados.
func descargarMontevideo(ctx context.Context, cliente *http.Client, base string) ([]*Tramite, error) {
	baseURL, err := url.Parse(strings.TrimRight(base, "/"))
	if err != nil {
		return nil, err
	}
	q := url.Values{}
	q.Set("filter[status]", "1")
	q.Set("page[limit]", "50")
	q.Set("include", "field_cmp_dep_responsable,field_cmp_categoria")
	q.Set("fields[node--tramite]", "drupal_internal__nid,title,created,changed,path,field_tra_descripcion,"+
		"field_tra_requisitos,field_tra_costo,field_tra_procedimiento,field_tra_aclaraciones,"+
		"field_cmp_dep_responsable,field_cmp_categoria")
	q.Set("fields[taxonomy_term--dependencia]", "name")
	q.Set("fields[taxonomy_term--categoria_tramite]", "name")
	siguiente := baseURL.String() + "/jsonapi/node/tramite?" + q.Encode()

	var tramites []*Tramite
	for paginas := 0; siguiente != "" && paginas < 200; paginas++ {
		crudo, err := descargarURL(ctx, cliente, siguiente)
		if err != nil {
			return nil, err
		}
		var r jsonAPIRespuesta
		if err := json.Unmarshal(crudo, &r); err != nil {
			return nil, fmt.Errorf("respuesta JSON:API inválida: %w", err)
		}
		nombres := map[refRecurso]string{}
		for _, inc := range r.Included {
			nombres[refRecurso{inc.Type, inc.ID}] = cadena(inc.Attributes["name"])
		}
		for _, rec := range r.Data {
			tramites = append(tramites, convertirTramiteIM(rec, nombres, baseURL))
		}

		siguiente = ""
		if r.Links.Next != nil && r.Links.Next.Href != "" {
			// El enlace "next" viene con http://; se conserva sólo la query sobre la URL base.
			sig, err := url.Parse(r.Links.Next.Href)
			if err != nil {
				return nil, err
			}
			siguiente = baseURL.String() + sig.Path + "?" + sig.RawQuery
		}
	}
	if len(tramites) == 0 {
		return nil, fmt.Errorf("la API de %s no devolvió trámites", nombreIM)
	}
	return tramites, nil
}

func convertirTramiteIM(rec jsonAPIRecurso, nombres map[refRecurso]string, base *url.URL) *Tramite {
	a := rec.Attributes
	t := &Tramite{
		Fuente:              nombreIM,
		Nombre:              strings.TrimSpace(cadena(a["title"])),
		InstitucionPadre:    nombreIM,
		InstitucionPadreOID: "im",
		Creado:              fecha(cadena(a["created"])),
		QueEs:               htmlATexto(textoFormateado(a["field_tra_descripcion"]), base),
		RequisitosGenerales: htmlATexto(textoFormateado(a["field_tra_requisitos"]), base),
		CostoTexto:          htmlATexto(textoFormateado(a["field_tra_costo"]), base),
		ComoSeHace:          htmlATexto(textoFormateado(a["field_tra_procedimiento"]), base),
		OtrosDatosDeInteres: htmlATexto(textoFormateado(a["field_tra_aclaraciones"]), base),
	}

	var nid int
	_ = json.Unmarshal(a["drupal_internal__nid"], &nid)
	t.ID = fmt.Sprintf("im-%d", nid)

	var cambiado struct {
		Value string `json:"value"`
	}
	if json.Unmarshal(a["changed"], &cambiado) == nil {
		t.Actualizado = fecha(cambiado.Value)
	}

	var ruta struct {
		Alias string `json:"alias"`
	}
	_ = json.Unmarshal(a["path"], &ruta)
	if ruta.Alias == "" {
		ruta.Alias = fmt.Sprintf("/node/%d", nid)
	}
	t.URL = base.String() + ruta.Alias

	if refs := referencias(rec, "field_cmp_dep_responsable"); len(refs) > 0 {
		t.Institucion = nombres[refs[0]]
		t.InstitucionOID = "im-" + refs[0].ID
	}
	if t.Institucion == "" {
		t.Institucion, t.InstitucionOID = nombreIM, "im"
	}
	var categorias []string
	for _, ref := range referencias(rec, "field_cmp_categoria") {
		if n := nombres[ref]; n != "" {
			categorias = append(categorias, n)
		}
	}
	t.CategoriasDeOrganismo = strings.Join(categorias, ", ")

	// El costo es texto libre; sólo "No tiene costo..." se considera gratuito.
	costoN := normalizar(t.CostoTexto)
	if costoN != "" && !strings.HasPrefix(costoN, "no tiene costo") && !strings.HasPrefix(costoN, "sin costo") {
		t.TieneCosto = "1"
	}
	return t
}

// referencias devuelve las entidades relacionadas (la relación puede ser simple o múltiple).
func referencias(rec jsonAPIRecurso, campo string) []refRecurso {
	crudo := rec.Relationships[campo].Data
	var muchas []refRecurso
	if json.Unmarshal(crudo, &muchas) == nil {
		return muchas
	}
	var una refRecurso
	if json.Unmarshal(crudo, &una) == nil && una.ID != "" {
		return []refRecurso{una}
	}
	return nil
}

func cadena(crudo json.RawMessage) string {
	var s string
	_ = json.Unmarshal(crudo, &s)
	return s
}

// textoFormateado lee un campo de texto de Drupal: string plano u objeto {value, processed}.
func textoFormateado(crudo json.RawMessage) string {
	if s := cadena(crudo); s != "" {
		return s
	}
	var f struct {
		Value     string `json:"value"`
		Processed string `json:"processed"`
	}
	if json.Unmarshal(crudo, &f) == nil {
		if f.Processed != "" {
			return f.Processed
		}
		return f.Value
	}
	return ""
}

// fecha convierte RFC 3339 a dd/mm/aaaa, el formato del CSV de AGESIC.
func fecha(s string) string {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return ""
	}
	return t.Format("02/01/2006")
}

var (
	reEnlace     = regexp.MustCompile(`(?is)<a\s[^>]*href="([^"]*)"[^>]*>(.*?)</a>`)
	reSaltoLinea = regexp.MustCompile(`(?i)<br\s*/?>|</(p|div|h[1-6]|tr|ul|ol)>`)
	reItem       = regexp.MustCompile(`(?i)<li[^>]*>`)
	reEtiqueta   = regexp.MustCompile(`<[^>]+>`)
	reEspacios   = regexp.MustCompile(`[ \t]+`)
	reLineasVac  = regexp.MustCompile(`\n{3,}`)
	reAntesItem  = regexp.MustCompile(`\n+- `)
)

// htmlATexto convierte el HTML de las fichas en texto legible, conservando enlaces y listas.
func htmlATexto(s string, base *url.URL) string {
	if s == "" {
		return ""
	}
	s = reEnlace.ReplaceAllStringFunc(s, func(m string) string {
		partes := reEnlace.FindStringSubmatch(m)
		href, texto := html.UnescapeString(partes[1]), strings.TrimSpace(reEtiqueta.ReplaceAllString(partes[2], ""))
		if strings.HasPrefix(href, "/") {
			href = base.String() + href
		}
		if href == "" || strings.HasPrefix(href, "#") || texto == href {
			return texto
		}
		return fmt.Sprintf("%s (%s)", texto, href)
	})
	s = reSaltoLinea.ReplaceAllString(s, "\n")
	s = reItem.ReplaceAllString(s, "\n- ")
	s = reEtiqueta.ReplaceAllString(s, "")
	s = html.UnescapeString(s)
	s = strings.NewReplacer(" ", " ", "\r", "").Replace(s)
	lineas := strings.Split(s, "\n")
	for i, l := range lineas {
		lineas[i] = strings.TrimSpace(reEspacios.ReplaceAllString(l, " "))
	}
	s = reLineasVac.ReplaceAllString(strings.Join(lineas, "\n"), "\n\n")
	s = reAntesItem.ReplaceAllString(s, "\n- ")
	return strings.TrimSpace(s)
}
