package theme

import (
	"strings"

	"github.com/egoist/mygo/ui"
)

type BotAvatar struct {
	ShapeID   string
	ColorID   string
	Body      *ui.SVG
	Eyes      *ui.SVG
	BodyColor ui.Color
	EyeColor  ui.Color
}

type botAvatarColor struct {
	id       string
	light    ui.Color
	dark     ui.Color
	eyeLight ui.Color
	eyeDark  ui.Color
}

type botAvatarShape struct {
	id   string
	body *ui.SVG
	eyes *ui.SVG
}

var botAvatarColors = [...]botAvatarColor{
	{id: "terracotta", light: ui.Hex("#E4542E"), dark: ui.Hex("#F06A46"), eyeLight: ui.Hex("#FFFDF4"), eyeDark: ui.Hex("#FFFDF4")},
	{id: "clay", light: ui.Hex("#8E613E"), dark: ui.Hex("#B38058"), eyeLight: ui.Hex("#FFFDF4"), eyeDark: ui.Hex("#FFFDF4")},
	{id: "marigold", light: ui.Hex("#DE830E"), dark: ui.Hex("#EB9A24"), eyeLight: ui.Hex("#FFFDF4"), eyeDark: ui.Hex("#FFFDF4")},
	{id: "alpine", light: ui.Hex("#2B72D6"), dark: ui.Hex("#4B93F2"), eyeLight: ui.Hex("#FFFDF4"), eyeDark: ui.Hex("#FFFDF4")},
	{id: "obsidian", light: ui.Hex("#222320"), dark: ui.Hex("#E8E5DA"), eyeLight: ui.Hex("#FFFDF4"), eyeDark: ui.Hex("#1D1E1B")},
	{id: "slate", light: ui.Hex("#666A68"), dark: ui.Hex("#8F9491"), eyeLight: ui.Hex("#FFFDF4"), eyeDark: ui.Hex("#FFFDF4")},
	{id: "jade", light: ui.Hex("#1F8E6E"), dark: ui.Hex("#2DAE88"), eyeLight: ui.Hex("#FFFDF4"), eyeDark: ui.Hex("#FFFDF4")},
	{id: "berry", light: ui.Hex("#D43F6E"), dark: ui.Hex("#EC5988"), eyeLight: ui.Hex("#FFFDF4"), eyeDark: ui.Hex("#FFFDF4")},
	{id: "violet", light: ui.Hex("#7559D4"), dark: ui.Hex("#9278EE"), eyeLight: ui.Hex("#FFFDF4"), eyeDark: ui.Hex("#FFFDF4")},
}

var botAvatarShapes = [...]botAvatarShape{
	{
		id:   "duo_stack",
		body: mustParseBotAvatarSVG(`<path d="M 36 14 L 50 13.2 C 56.5 13 60.5 17.5 60 23.5 C 59.5 29.2 54.5 32.2 46 32.5 L 35 32.5 C 28 32.5 24.5 28.8 25.5 23 C 26.5 17.5 30.5 14.2 36 14 Z"/><path d="M 25 36.5 L 53 35.5 C 62 35.2 68.5 41.5 69.5 50.5 L 70 57.5 C 70.3 63.8 64.5 67.2 55 67.2 L 24 67.2 C 14.5 67.2 9.8 63.2 10.5 56 L 11.8 47.5 C 13 40.5 18 36.8 25 36.5 Z"/>`),
		eyes: mustParseBotAvatarSVG(`<ellipse cx="33.5" cy="51.5" rx="2.9" ry="3.9"/><ellipse cx="46.5" cy="51.5" rx="2.9" ry="3.9"/>`),
	},
	{
		id:   "arch",
		body: mustParseBotAvatarSVG(`<path d="M 27 17.5 L 51 16.5 C 60 16.2 66.5 23.5 68.5 36.5 L 70 56 C 70.5 63.5 66.5 67.2 59.5 67.2 L 54.5 67.2 C 50.5 67.2 49.5 64 49 58.5 C 48.2 51 44.8 47.5 40 47.5 C 35.2 47.5 31.8 51 31 58.5 C 30.5 64 29.5 67.2 25.5 67.2 L 20.5 67.2 C 13.5 67.2 9.5 63.5 10 56 L 11.5 36.5 C 13.5 23.5 18.5 17.8 27 17.5 Z"/>`),
		eyes: mustParseBotAvatarSVG(`<ellipse cx="33.5" cy="32.5" rx="2.9" ry="3.9"/><ellipse cx="46.5" cy="32.5" rx="2.9" ry="3.9"/>`),
	},
	{
		id:   "mushroom",
		body: mustParseBotAvatarSVG(`<path d="M 24 14.5 L 55 13.5 C 65 13.2 71.5 19.5 71 29.5 C 70.5 38 64.5 43.2 55 44 C 52 44.3 50.5 46.8 51.5 51.5 L 55.5 60.5 C 57 64.5 54 67.2 49 67.2 L 29 67.2 C 24 67.2 21.5 64.5 23 60.5 L 27 51.5 C 28 46.8 26.5 44.3 23.5 44 C 14 43.2 8.5 38 9 29.5 C 9.5 20 15 14.8 24 14.5 Z"/>`),
		eyes: mustParseBotAvatarSVG(`<ellipse cx="33.5" cy="29.5" rx="2.9" ry="3.9"/><ellipse cx="46.5" cy="29.5" rx="2.9" ry="3.9"/>`),
	},
	{
		id:   "balanced",
		body: mustParseBotAvatarSVG(`<path d="M 28 14 L 54 11.5 C 63 10.8 68.5 17.5 67.5 28.5 L 65.5 38.5 C 64.2 45 58 48.2 48.5 48.5 L 26.5 47 C 17 46.2 12.5 40.5 13.8 31.5 L 16 23.5 C 17.5 17.2 21.8 14.6 28 14 Z"/><path d="M 35 51 L 46 51 C 51.5 51.2 55.5 56 57.5 61.5 C 59 65.2 56.5 67.2 52 67.2 L 28 67.2 C 23.5 67.2 21 65.2 22.5 61.5 C 24.5 56 29 51.2 35 51 Z"/>`),
		eyes: mustParseBotAvatarSVG(`<ellipse cx="34.5" cy="30.5" rx="2.9" ry="3.9" transform="rotate(-5 34.5 30.5)"/><ellipse cx="47.2" cy="29.2" rx="2.9" ry="3.9" transform="rotate(-5 47.2 29.2)"/>`),
	},
	{
		id:   "split",
		body: mustParseBotAvatarSVG(`<path d="M 25 18 L 43 17 C 49.5 16.8 52 21 50.5 27.5 L 44.5 62 C 43.8 65.8 41 67.2 37 67.2 L 21 67.2 C 13.5 67.2 9.5 62 10.2 53 L 12 33 C 13 23.5 18 18.4 25 18 Z"/><path d="M 55.5 20.5 C 62 21.5 67.5 27.5 69 38.5 L 70 55.5 C 70.5 62.5 66.5 67.2 59.5 67.2 L 50.5 67.2 C 48.2 67.2 47.2 65.5 47.8 62.5 L 53.5 24.5 C 53.9 22 54.5 20.3 55.5 20.5 Z"/>`),
		eyes: mustParseBotAvatarSVG(`<ellipse cx="23.5" cy="43.5" rx="2.78" ry="3.74"/><ellipse cx="35.5" cy="43.5" rx="2.78" ry="3.74"/>`),
	},
	{
		id:   "slate",
		body: mustParseBotAvatarSVG(`<path d="M 27 16.5 L 56 15.8 C 61.5 15.6 64.5 19 64 23.5 C 63.5 27.8 60 29.5 54 29.5 L 26 29.5 C 20.5 29.5 18 26.8 18.8 22.2 C 19.5 18.2 22.5 16.7 27 16.5 Z"/><path d="M 19 32.8 L 54 32.5 C 59.8 32.5 62.8 35.8 62.2 40.5 C 61.6 45 58 46.8 52 46.8 L 18 46.8 C 12.2 46.8 9.8 43.8 10.6 39 C 11.4 34.8 14.5 32.8 19 32.8 Z"/><path d="M 21 49.8 L 61 49.5 C 67 49.5 70.5 53.5 70 59.5 C 69.5 65 65 67.2 58 67.2 L 21 67.2 C 14.5 67.2 11.5 64 12.2 58.2 C 12.8 52.8 16 49.8 21 49.8 Z"/>`),
		eyes: mustParseBotAvatarSVG(`<ellipse cx="35.0" cy="58.4" rx="2.49" ry="3.35"/><ellipse cx="47.2" cy="58.4" rx="2.49" ry="3.35"/>`),
	},
	{
		id:   "yardang",
		body: mustParseBotAvatarSVG(`<path d="M 23 17 L 48 17 C 53 17 56.2 20.5 57 26.5 L 58.2 35.5 C 58.6 37.8 60.2 38.8 62.5 39.2 C 67.5 40 70.5 44.5 70.5 52 L 70.5 58.5 C 70.5 64.2 66.5 67.2 60 67.2 L 19 67.2 C 12.5 67.2 9.2 63.5 10 56 L 14.5 25.5 C 15.5 19.8 18.5 17 23 17 Z"/>`),
		eyes: mustParseBotAvatarSVG(`<ellipse cx="29.5" cy="43.0" rx="2.9" ry="3.9"/><ellipse cx="42.5" cy="43.0" rx="2.9" ry="3.9"/>`),
	},
	{
		id:   "twins",
		body: mustParseBotAvatarSVG(`<path d="M 44 18.5 L 55 18 C 63 17.8 68.5 24 69.5 36.5 L 70 55 C 70.3 63 65 67.2 56.5 67.2 L 40.5 67.2 C 33.5 67.2 30.2 62.5 30.8 54 L 32.5 33.5 C 33.5 23.5 37.8 18.8 44 18.5 Z"/><path d="M 17.5 41.5 L 22 41.2 C 26.5 41.2 28.8 45.5 28.5 53 L 28.2 59.5 C 28 64.8 25 67.2 20 67.2 L 15.5 67.2 C 10.5 67.2 8.2 63.5 9 56.5 L 10 49.5 C 10.8 44 13.5 41.6 17.5 41.5 Z"/>`),
		eyes: mustParseBotAvatarSVG(`<ellipse cx="44.5" cy="42.5" rx="2.78" ry="3.74"/><ellipse cx="56.5" cy="42.5" rx="2.78" ry="3.74"/>`),
	},
}

func mustParseBotAvatarSVG(inner string) *ui.SVG {
	return ui.MustParseSVG([]byte(`<svg viewBox="0 0 80 80" fill="currentColor" xmlns="http://www.w3.org/2000/svg">` + inner + `</svg>`))
}

func ResolveBotAvatar(t *ui.Theme, id, name string, builtin bool) BotAvatar {
	shapeIdx, colorIdx := 0, 0
	if !builtin && !strings.EqualFold(strings.TrimSpace(name), "PandoraBox") {
		key := strings.TrimSpace(id)
		if key == "" {
			key = strings.TrimSpace(name)
		}
		shapeIdx = int(botAvatarHash("shape:"+key) % uint32(len(botAvatarShapes)))
		colorIdx = int(botAvatarHash("color:"+key) % uint32(len(botAvatarColors)))
	}
	shape := botAvatarShapes[shapeIdx]
	color := botAvatarColors[colorIdx]
	bodyColor, eyeColor := color.light, color.eyeLight
	if t != nil && t.Dark {
		bodyColor, eyeColor = color.dark, color.eyeDark
	}
	return BotAvatar{
		ShapeID:   shape.id,
		ColorID:   color.id,
		Body:      shape.body,
		Eyes:      shape.eyes,
		BodyColor: bodyColor,
		EyeColor:  eyeColor,
	}
}

func BotAvatarSVGs() []*ui.SVG {
	svgs := make([]*ui.SVG, 0, len(botAvatarShapes)*2)
	for _, shape := range botAvatarShapes {
		svgs = append(svgs, shape.body, shape.eyes)
	}
	return svgs
}

func botAvatarHash(key string) uint32 {
	hash := uint32(2166136261)
	for i := 0; i < len(key); i++ {
		hash = (hash ^ uint32(key[i])) * 16777619
	}
	return hash ^ (hash >> 16)
}
