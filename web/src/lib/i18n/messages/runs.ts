// Messages de l'espace de noms « runs ». Clés préfixées par « runs. ».
// `en` doit contenir exactement les clés de `fr` (vérifié par TypeScript).

export const fr = {} satisfies Record<string, string>;

export const en: Record<keyof typeof fr, string> = {};
