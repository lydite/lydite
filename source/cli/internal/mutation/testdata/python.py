# A fixture whose only job is to hold every operator in the catalogue still.
# Nothing runs it: a golden mutant set is about what the grammar sees.


def grade(score, bonus):
    total = score + bonus
    if total < 50:
        log(total)
        return 0
    if total == 100:
        return 100
    return total


def passed(score):
    if score >= 50:
        return True
    if score > 49:  # [lydite:exclude_from_mutation][the two spellings of this bound agree for every score]
        return False
    return False


def banded(low, score, high):
    # A chain is one comparison_operator node carrying a token per link, and
    # each token is its own mutant at its own byte range.
    #
    # An exclusion declaration on this line would cover more than one of them:
    # it attaches to every mutant tied for the shortest replaced text, and two
    # single-character bounds tie with each other as well as with their own
    # negations. A chain cannot have one of its bounds acknowledged and the
    # other left asked about.
    return low < score < high


def label(score, bonus):
    scaled = score * 2 / 3
    counter += 1
    if scaled != 0 and bonus:
        return "pass"
    if scaled or bonus:
        return ""
    return 1.5
