fn days_from_civil(year: i64, month: i64, day: i64) -> i64 {
    let year = if month <= 2 { year - 1 } else { year };
    let era = if year >= 0 { year } else { year - 399 } / 400;
    let of_era = year - era * 400;
    let of_year = (153 * (month + if month > 2 { -3 } else { 9 }) + 2) / 5 + day - 1;
    let of_cycle = of_era * 365 + of_era / 4 - of_era / 100 + of_year;
    era * 146_097 + of_cycle - 719_468
}

fn days_in_month(year: i64, month: i64) -> i64 {
    match month {
        2 if (year % 4 == 0 && year % 100 != 0) || year % 400 == 0 => 29,
        2 => 28,
        4 | 6 | 9 | 11 => 30,
        _ => 31,
    }
}

fn digits(text: &str, width: usize) -> Option<i64> {
    (text.len() == width && text.bytes().all(|byte| byte.is_ascii_digit()))
        .then(|| text.parse().ok())
        .flatten()
}

fn parse(text: &str) -> Option<i64> {
    let (date, time) = text.split_once('T')?;
    let mut date = date.split('-');
    let year = digits(date.next()?, 4)?;
    let month = digits(date.next()?, 2)?;
    let day = digits(date.next()?, 2)?;
    if date.next().is_some() {
        return None;
    }
    let (clock, offset) = if let Some(clock) = time.strip_suffix('Z') {
        (clock, 0)
    } else {
        let at = time.rfind(['+', '-'])?;
        let (clock, zone) = time.split_at(at);
        let sign = if zone.starts_with('-') { -1 } else { 1 };
        let (hours, minutes) = zone[1..].split_once(':')?;
        let (hours, minutes) = (digits(hours, 2)?, digits(minutes, 2)?);
        if hours > 23 || minutes > 59 {
            return None;
        }
        (clock, sign * (hours * 60 + minutes))
    };
    let (whole, fraction) = match clock.split_once('.') {
        Some((whole, fraction)) => (whole, Some(fraction)),
        None => (clock, None),
    };
    let mut whole = whole.split(':');
    let hour = digits(whole.next()?, 2)?;
    let minute = digits(whole.next()?, 2)?;
    let second = digits(whole.next()?, 2)?;
    if whole.next().is_some() {
        return None;
    }
    let millis = match fraction {
        None => 0,
        Some(fraction)
            if !fraction.is_empty() && fraction.bytes().all(|byte| byte.is_ascii_digit()) =>
        {
            format!("{fraction:0<3}")[..3].parse::<i64>().ok()?
        }
        Some(_) => return None,
    };
    if !(1..=12).contains(&month)
        || day < 1
        || day > days_in_month(year, month)
        || hour > 23
        || minute > 59
        || second > 59
    {
        return None;
    }
    let seconds = days_from_civil(year, month, day) * 86_400 + hour * 3600 + minute * 60 + second
        - offset * 60;
    Some(seconds * 1000 + millis)
}

pub fn rfc3339_millis(text: &str) -> i64 {
    parse(text).unwrap_or(0)
}
