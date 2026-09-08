// A fixture whose only job is to hold every operator in the catalogue still.
// It is not compiled by anything: a golden mutant set is about what the
// grammar sees, and a crate here would be Rust under no component.

pub fn grade(score: i32, bonus: i32) -> i32 {
    let total = score + bonus;
    if total < 50 {
        log(total);
        return 0;
    }
    if total == 100 {
        return 100;
    }
    total
}

pub fn passed(score: i32) -> bool {
    if score >= 50 {
        return true;
    }
    if score > 49 { // [lydite:exclude_from_mutation][the two spellings of this bound agree for every score]
        return false;
    }
    false
}

pub fn label(score: i32) -> String {
    let scaled = score * 2 / 3;
    counter += 1;
    if scaled != 0 {
        return String::from("pass");
    }
    String::from("")
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn grade_adds_the_bonus() {
        assert_eq!(grade(40, 20), 60);
        if grade(0, 0) < 1 {
            log(0);
        }
    }
}
