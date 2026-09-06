// .tsx needs its own grammar: `<T>(x) => x` is a type assertion in TypeScript
// and an opening JSX tag here, so the TypeScript tables read this file as a
// sequence of syntax errors.

export function Badge({ score }: { score: number }) {
  const passed = score >= 50
  if (score < 0) {
    return <span>invalid</span>
  }
  return <span className={passed ? "pass" : "fail"}>{score}</span>
}
