package main

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"unicode"
)

var sinTildes = strings.NewReplacer(
	"á", "a", "é", "e", "í", "i", "ó", "o", "ú", "u", "ü", "u", "ñ", "n",
	"à", "a", "è", "e", "ì", "i", "ò", "o", "ù", "u",
)

// normalizar pasa a minúsculas, quita tildes y deja sólo letras, dígitos y espacios.
func normalizar(s string) string {
	s = sinTildes.Replace(strings.ToLower(s))
	return strings.Join(strings.FieldsFunc(s, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}), " ")
}

var palabrasVacias = map[string]bool{
	"de": true, "la": true, "el": true, "los": true, "las": true, "del": true, "en": true,
	"y": true, "o": true, "a": true, "un": true, "una": true, "por": true, "para": true,
	"con": true, "al": true, "mi": true, "se": true, "que": true, "como": true, "tramite": true,
}

// siglas arma la sigla de un nombre con las iniciales de sus palabras significativas:
// "Banco de Previsión Social" -> "bps".
func siglas(nombre string) string {
	var b strings.Builder
	for _, p := range strings.Fields(normalizar(nombre)) {
		if !palabrasVacias[p] {
			b.WriteByte(p[0])
		}
	}
	if b.Len() < 2 {
		return ""
	}
	return b.String()
}

// coincideInstitucion acepta una sigla exacta ("bps") o palabras del nombre ("intendencia",
// "identificacion civil"). Los filtros cortos (hasta 4 letras) se tratan como sigla o palabra
// completa, para que "im" no coincida con "maritima" o "impositiva".
func coincideInstitucion(t *Tramite, filtroN string) bool {
	if slices.Contains(t.siglas, filtroN) {
		return true
	}
	if len(filtroN) <= 4 && !strings.Contains(filtroN, " ") {
		return strings.Contains(" "+t.institucionN+" ", " "+filtroN+" ")
	}
	return contienePalabra(t.institucionN, filtroN)
}

func terminos(consulta string) []string {
	var ts []string
	for _, p := range strings.Fields(normalizar(consulta)) {
		if !palabrasVacias[p] {
			ts = append(ts, p)
		}
	}
	return ts
}

// contienePalabra busca el término como prefijo de alguna palabra del texto
// ("licencia" encuentra "licencias", "conducir" encuentra "conducir").
func contienePalabra(texto, termino string) bool {
	return strings.HasPrefix(texto, termino) || strings.Contains(texto, " "+termino)
}

type FiltrosBusqueda struct {
	Consulta    string
	Institucion string
	Canal       string // internet | persona | telefono
	SoloGratis  bool
	Limite      int
}

type resultado struct {
	t       *Tramite
	puntaje int
}

// Buscar devuelve los trámites que contienen todos los términos de la consulta
// (en nombre, institución o descripción), ordenados por relevancia.
func Buscar(d *Datos, f FiltrosBusqueda) (encontrados []*Tramite, total int) {
	ts := terminos(f.Consulta)
	inst := normalizar(f.Institucion)
	canal := normalizar(f.Canal)

	var res []resultado
	for _, t := range d.Tramites {
		if inst != "" && !coincideInstitucion(t, inst) {
			continue
		}
		if canal != "" && !strings.Contains(normalizar(t.CanalesDeAtencion), canal) {
			continue
		}
		if f.SoloGratis && t.TieneCosto == "1" {
			continue
		}
		puntaje, ok := 0, true
		for _, term := range ts {
			switch {
			case contienePalabra(t.nombreN, term):
				puntaje += 10
			case contienePalabra(t.institucionN, term):
				puntaje += 4
			case contienePalabra(t.textoN, term):
				puntaje += 2
			default:
				ok = false
			}
			if !ok {
				break
			}
		}
		if !ok {
			continue
		}
		if len(ts) > 0 && strings.Contains(t.nombreN, strings.Join(ts, " ")) {
			puntaje += 15 // la frase completa aparece en el nombre
		}
		res = append(res, resultado{t, puntaje})
	}

	sort.SliceStable(res, func(i, j int) bool {
		if res[i].puntaje != res[j].puntaje {
			return res[i].puntaje > res[j].puntaje
		}
		return len(res[i].t.Nombre) < len(res[j].t.Nombre)
	})

	limite := f.Limite
	if limite <= 0 {
		limite = 10
	}
	limite = min(limite, 50)
	for i := 0; i < len(res) && i < limite; i++ {
		encontrados = append(encontrados, res[i].t)
	}
	return encontrados, len(res)
}

type InstitucionConteo struct {
	Nombre   string
	Padre    string
	Tramites int
}

func ListarInstituciones(d *Datos, filtro string) []InstitucionConteo {
	filtroN := normalizar(filtro)
	conteo := map[string]*InstitucionConteo{}
	for _, t := range d.Tramites {
		if filtroN != "" && !coincideInstitucion(t, filtroN) {
			continue
		}
		// Se agrupa por OID: hay nombres repetidos en distintos organismos ("Intendencia" en 11 departamentos).
		clave := t.InstitucionOID + "|" + t.Institucion
		c, ok := conteo[clave]
		if !ok {
			c = &InstitucionConteo{Nombre: t.Institucion, Padre: t.InstitucionPadre}
			conteo[clave] = c
		}
		c.Tramites++
	}
	lista := make([]InstitucionConteo, 0, len(conteo))
	for _, c := range conteo {
		lista = append(lista, *c)
	}
	sort.Slice(lista, func(i, j int) bool {
		if lista[i].Tramites != lista[j].Tramites {
			return lista[i].Tramites > lista[j].Tramites
		}
		if lista[i].Nombre != lista[j].Nombre {
			return lista[i].Nombre < lista[j].Nombre
		}
		return lista[i].Padre < lista[j].Padre
	})
	return lista
}

func recortar(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func FormatearResumen(t *Tramite) string {
	var b strings.Builder
	fmt.Fprintf(&b, "- **%s** (id: %s)\n  Institución: %s", t.Nombre, t.ID, t.Institucion)
	if t.InstitucionPadre != "" && t.InstitucionPadre != t.Institucion {
		fmt.Fprintf(&b, " — %s", t.InstitucionPadre)
	}
	if t.CanalesDeAtencion != "" {
		fmt.Fprintf(&b, "\n  Canales: %s", t.CanalesDeAtencion)
	}
	fmt.Fprintf(&b, "\n  Costo: %s", textoCosto(t))
	if t.QueEs != "" {
		fmt.Fprintf(&b, "\n  %s", recortar(t.QueEs, 220))
	}
	if t.URL != "" {
		fmt.Fprintf(&b, "\n  %s", t.URL)
	}
	return b.String()
}

func textoCosto(t *Tramite) string {
	if t.CostoTexto != "" {
		return recortar(t.CostoTexto, 160)
	}
	if t.TieneCosto != "1" {
		return "sin costo informado"
	}
	partes := []string{"tiene costo"}
	if t.Valor != "" || (t.Unidad != "" && t.Unidad != "Sin definir") {
		partes = append(partes, strings.TrimSpace(t.Unidad+" "+t.Valor))
	}
	return strings.Join(partes, ": ")
}

// FormatearFicha arma la ficha completa en Markdown, omitiendo campos vacíos.
func FormatearFicha(t *Tramite) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", t.Nombre)
	seccion := func(titulo, valor string) {
		valor = strings.TrimSpace(valor)
		if valor != "" {
			fmt.Fprintf(&b, "## %s\n%s\n\n", titulo, strings.ReplaceAll(valor, " | ", "\n- "))
		}
	}
	linea := func(etiqueta, valor string) {
		if strings.TrimSpace(valor) != "" {
			fmt.Fprintf(&b, "- **%s:** %s\n", etiqueta, strings.TrimSpace(valor))
		}
	}
	linea("ID", t.ID)
	linea("Institución", t.Institucion)
	if t.InstitucionPadre != t.Institucion {
		linea("Organismo", t.InstitucionPadre)
	}
	linea("Ficha oficial", t.URL)
	linea("Canales de atención", t.CanalesDeAtencion)
	if t.CostoTexto == "" {
		linea("Costo", textoCosto(t))
	}
	linea("Email de consulta", t.EmailDeConsulta)
	linea("Categoría", t.CategoriasDeOrganismo)
	linea("Creado", t.Creado)
	linea("Actualizado", t.Actualizado)
	linea("Fuente", t.Fuente)
	b.WriteString("\n")

	seccion("¿Qué es?", t.QueEs)
	seccion("Requisitos", t.RequisitosGenerales)
	seccion("Costo", t.CostoTexto)
	seccion("¿Cómo se hace?", t.ComoSeHace)
	seccion("Otros costos", t.OtrosCostos)
	seccion("Otros datos de interés", t.OtrosDatosDeInteres)
	if t.InternetURL != "" || t.InternetComoSeHace != "" || t.InternetRequisitos != "" {
		seccion("En línea — requisitos", t.InternetRequisitos)
		seccion("En línea — ¿cómo se hace?", t.InternetComoSeHace)
		seccion("En línea — iniciar trámite", t.InternetURL)
	}
	seccion("En persona — requisitos", t.PersonaRequisitos)
	seccion("En persona — ¿cómo se hace?", t.PersonaComoSeHace)
	seccion("En persona — direcciones", t.PersonaDirecciones)
	seccion("En persona — otras direcciones", t.PersonaOtrasDirecc)
	seccion("Por teléfono — requisitos", t.TelefonoRequisitos)
	seccion("Por teléfono — ¿cómo se hace?", t.TelefonoComoSeHace)
	seccion("Más información", t.MasInfoVinculos)
	seccion("Documentos adjuntos", t.MasInfoAdjuntos)
	return strings.TrimSpace(b.String())
}
