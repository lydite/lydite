// A fixture whose only job is to hold every operator in the catalogue still.
// Nothing runs it: a golden mutant set is about what the grammar sees.

export function grade(score: number, bonus: number): number {
  const total = score + bonus
  if (total < 50) {
    log(total)
    return 0
  }
  if (total === 100) {
    return 100
  }
  return total
}

export function passed(score: number): boolean {
  if (score >= 50) {
    return true
  }
  if (score > 49) { // [lydite:exclude_from_mutation][the two spellings of this bound agree for every score]
    return false
  }
  return false
}

export function label(score: number): string {
  const scaled = score * 2 / 3
  counter += 1
  counter++
  if (scaled !== 0) {
    return "pass"
  }
  return ""
}
