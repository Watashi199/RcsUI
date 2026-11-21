// Messages de l'espace de noms « admin ». Clés préfixées par « admin. ».
// `en` doit contenir exactement les clés de `fr` (vérifié par TypeScript).

export const fr = {} satisfies Record<string, string>;

export const en: Record<keyof typeof fr, string> = {};
